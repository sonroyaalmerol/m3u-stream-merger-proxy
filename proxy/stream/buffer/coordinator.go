package buffer

import (
	"container/ring"
	"context"
	"errors"
	"fmt"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/proxy/loadbalancer"
	"m3u-stream-merger/proxy/stream/config"
	"m3u-stream-merger/store"
	"sync"
	"sync/atomic"
	"time"

	"github.com/valyala/bytebufferpool"
)

type ChunkData struct {
	Buffer    *bytebufferpool.ByteBuffer
	Error     error
	Status    int
	Timestamp time.Time

	seq int64 // unexported sequence number for internal tracking.
}

func newChunkData() *ChunkData {
	return &ChunkData{
		Buffer: bytebufferpool.Get(),
		seq:    0,
	}
}

// NOTE: Clients must call Reset() on each ChunkData once done with it.
// Failure to do so may cause the underlying bytebufferpool to deplete.
func (c *ChunkData) Reset() {
	if c.Buffer != nil {
		c.Buffer.Reset()
		bytebufferpool.Put(c.Buffer)
	}
	c.Buffer = bytebufferpool.Get()
	c.Error = nil
	c.Status = 0
	c.Timestamp = time.Time{}
	c.seq = 0
}

// internal stream state constants to prevent races between writer shutdown
// and new registrations.
const (
	stateActive int32 = iota
	stateDraining
	stateClosed
)

var (
	ErrStreamClosed   = errors.New("stream is closed")
	ErrStreamDraining = errors.New("stream is draining")
)

type StreamCoordinator struct {
	Buffer       *ring.Ring
	mu           sync.RWMutex
	broadcast    chan struct{}
	ClientCount  int32
	WriterCtx    context.Context
	WriterCancel context.CancelFunc
	WriterChan   chan struct{}
	WriterCtxMu  sync.Mutex
	WriterActive atomic.Bool

	lastError atomic.Value
	logger    logger.Logger
	config    *config.StreamConfig
	cm        *store.ConcurrencyManager
	streamID  string

	// state now represents our three‐state (active, draining, closed) machine.
	state int32

	LBResultOnWrite atomic.Pointer[loadbalancer.LoadBalancerResult]

	WasInvalid atomic.Bool

	// writeSeq is an atomic counter that is assigned to each chunk written.
	writeSeq int64
}

// subscribe returns the current broadcast channel.
func (c *StreamCoordinator) subscribe() <-chan struct{} {
	c.mu.RLock()
	ch := c.broadcast
	c.mu.RUnlock()
	return ch
}

// notifySubscribers atomically closes the current broadcast channel
// and creates a new one so that waiting clients are woken.
func (c *StreamCoordinator) notifySubscribers() {
	c.mu.Lock()
	// Close the current broadcast channel.
	close(c.broadcast)
	// Create a new broadcast channel for subsequent subscribers.
	c.broadcast = make(chan struct{})
	c.mu.Unlock()
}

func NewStreamCoordinator(streamID string, config *config.StreamConfig, cm *store.ConcurrencyManager, logger logger.Logger) *StreamCoordinator {
	logger.Debug("Initializing new StreamCoordinator")

	r := ring.New(config.SharedBufferSize)
	for i := 0; i < config.SharedBufferSize; i++ {
		r.Value = newChunkData()
		r = r.Next()
	}

	coord := &StreamCoordinator{
		Buffer:     r,
		WriterChan: make(chan struct{}, 1),
		logger:     logger,
		config:     config,
		cm:         cm,
		streamID:   streamID,
		broadcast:  make(chan struct{}),
	}
	atomic.StoreInt32(&coord.state, stateActive)
	coord.lastError.Store((*ChunkData)(nil))

	logger.Debugf("StreamCoordinator initialized with buffer size: %d, chunk size: %d",
		config.SharedBufferSize, config.ChunkSize)
	return coord
}

func (c *StreamCoordinator) RegisterClient() error {
	state := atomic.LoadInt32(&c.state)

	// If stream is closed but there are no clients, allow reset
	if state == stateClosed && atomic.LoadInt32(&c.ClientCount) == 0 {
		if atomic.CompareAndSwapInt32(&c.state, stateClosed, stateActive) {
			c.logger.Debug("Resetting closed stream to active state")
			state = stateActive
		}
	}

	if state != stateActive {
		c.logger.Warn("Attempt to register client on non-active stream")
		switch state {
		case stateDraining:
			return ErrStreamDraining
		default:
			return ErrStreamClosed
		}
	}

	count := atomic.AddInt32(&c.ClientCount, 1)
	c.logger.Debugf("Client registered. Total clients: %d", count)
	return nil
}

func (c *StreamCoordinator) UnregisterClient() {
	count := atomic.AddInt32(&c.ClientCount, -1)
	c.logger.Debugf("Client unregistered. Remaining clients: %d", count)

	if count <= 0 {
		c.logger.Debug("Last client unregistered, initiating stream shutdown")
		c.initiateShutdown()
	}
}

func (c *StreamCoordinator) initiateShutdown() {
	if !atomic.CompareAndSwapInt32(&c.state, stateActive, stateDraining) {
		return // Already draining or closed
	}

	c.WriterCtxMu.Lock()
	if c.WriterCancel != nil {
		c.WriterCancel()
		c.WriterCancel = nil
	}
	c.WriterCtxMu.Unlock()

	c.WriterActive.Store(false)
	c.clearBuffer()
	atomic.StoreInt32(&c.state, stateClosed)
	c.notifySubscribers()
}

func (c *StreamCoordinator) HasClient() bool {
	count := atomic.LoadInt32(&c.ClientCount)
	return count > 0
}

func (c *StreamCoordinator) GetWriterLBResult() *loadbalancer.LoadBalancerResult {
	return c.LBResultOnWrite.Load()
}

// shouldTimeout uses lastSuccess rather than a fixed start time so that a
// genuine period of inactivity is detected.
func (c *StreamCoordinator) shouldTimeout(lastSuccess time.Time, timeout time.Duration) bool {
	shouldTimeout := c.config.TimeoutSeconds > 0 && time.Since(lastSuccess) >= timeout
	if shouldTimeout {
		c.logger.Debugf("Stream timed out after %v", time.Since(lastSuccess))
	}
	return shouldTimeout
}

func (c *StreamCoordinator) shouldRetry(timeout time.Duration) bool {
	return c.config.TimeoutSeconds == 0 || timeout > 0
}

func (c *StreamCoordinator) Write(chunk *ChunkData) bool {
	if chunk == nil {
		c.logger.Debug("Write: Received nil chunk")
		return false
	}

	c.mu.Lock()
	// Check if we are still active.
	if atomic.LoadInt32(&c.state) != stateActive {
		c.logger.Debug("Write: Stream not active")
		c.mu.Unlock()
		return false
	}

	current, ok := c.Buffer.Value.(*ChunkData)
	if !ok || current == nil {
		c.logger.Debug("Write: Current buffer position is nil")
		c.mu.Unlock()
		return false
	}

	// Update sequence number so each written chunk may be tracked.
	seq := atomic.AddInt64(&c.writeSeq, 1)
	current.seq = seq

	// Swap buffers to avoid copying
	oldBuffer := current.Buffer
	current.Buffer = chunk.Buffer
	chunk.Buffer = oldBuffer // Caller is responsible for calling Reset() on the chunk

	current.Error = chunk.Error
	current.Status = chunk.Status
	current.Timestamp = chunk.Timestamp

	c.Buffer = c.Buffer.Next()
	c.logger.Debug("Write: Advanced buffer position")

	// If an error occurred in the chunk, store it (only once) and mark stream draining.
	if current.Error != nil || current.Status != 0 {
		if c.lastError.Load() == nil {
			c.lastError.Store(current)
		}
		atomic.StoreInt32(&c.state, stateDraining)
		c.logger.Debugf("Write: Setting error state: err=%v, status=%d", current.Error, current.Status)
	}
	c.mu.Unlock()

	// Notify subscribers outside the lock to avoid deadlock.
	c.notifySubscribers()
	return true
}

func (c *StreamCoordinator) ReadChunks(fromPosition *ring.Ring) ([]*ChunkData, *ChunkData, *ring.Ring) {
	c.mu.RLock()
	if fromPosition == nil {
		c.logger.Debug("ReadChunks: fromPosition is nil, using current buffer")
		fromPosition = c.Buffer
	}
	// Check whether the client pointer is too far behind.
	if cd, ok := fromPosition.Value.(*ChunkData); ok && cd != nil {
		currentWriteSeq := atomic.LoadInt64(&c.writeSeq)
		minSeq := currentWriteSeq - int64(c.config.SharedBufferSize)
		if cd.seq < minSeq {
			c.logger.Warn("ReadChunks: Client pointer is stale; " +
				"resetting to the latest chunk and returning a stale error")
			errorChunk := &ChunkData{
				Buffer:    nil,
				Error:     fmt.Errorf("data lost due to slow consumer; read pointer reset"),
				Status:    proxy.StatusServerError,
				Timestamp: time.Now(),
			}
			c.mu.RUnlock()
			// Return the latest pointer so the caller may resubscribe.
			return nil, errorChunk, c.Buffer
		}
	}

	// If we've caught up with the writer and the stream is active, wait.
	for fromPosition == c.Buffer && atomic.LoadInt32(&c.state) == stateActive {
		c.mu.RUnlock()
		ch := c.subscribe()
		<-ch // Wait until the broadcast channel is closed.
		c.mu.RLock()
	}

	chunks := make([]*ChunkData, 0, c.config.SharedBufferSize)
	current := fromPosition
	var errorFound bool
	var errorChunk *ChunkData

	// Iterate until we reach the writer’s current position.
	for current != c.Buffer {
		if chunk, ok := current.Value.(*ChunkData); ok && chunk != nil {
			if chunk.Buffer != nil && chunk.Buffer.Len() > 0 {
				newChunk := &ChunkData{
					Buffer:    bytebufferpool.Get(),
					Timestamp: chunk.Timestamp,
				}
				_, _ = newChunk.Buffer.Write(chunk.Buffer.Bytes())
				chunks = append(chunks, newChunk)
			}
			if chunk.Error != nil || chunk.Status != 0 {
				errorFound = true
				errorChunk = &ChunkData{
					Buffer:    nil,
					Error:     chunk.Error,
					Status:    chunk.Status,
					Timestamp: chunk.Timestamp,
				}
			}
		}
		current = current.Next()
		// In a well‐formed ring, current will eventually equal c.Buffer.
		if current == fromPosition {
			break
		}
	}
	c.mu.RUnlock()

	// If any chunk in the ring signaled an error, return it.
	if errorFound && errorChunk != nil {
		return chunks, errorChunk, current
	}

	if lastErr := c.lastError.Load(); lastErr != nil {
		if errChunk, ok := lastErr.(*ChunkData); ok && errChunk != nil {
			return chunks, errChunk, current
		}
	}

	return chunks, nil, current
}

func (c *StreamCoordinator) clearBuffer() {
	c.mu.Lock()
	defer c.mu.Unlock()

	current := c.Buffer
	for i := 0; i < c.config.SharedBufferSize; i++ {
		if chunk, ok := current.Value.(*ChunkData); ok {
			chunk.Reset()
		}
		current = current.Next()
	}
}

func (c *StreamCoordinator) getTimeoutDuration() time.Duration {
	if c.config.TimeoutSeconds == 0 {
		return time.Minute
	}
	return time.Duration(c.config.TimeoutSeconds) * time.Second
}

func (c *StreamCoordinator) writeError(err error, status int) {
	chunk := newChunkData()
	if chunk == nil {
		c.logger.Debug("writeError: Failed to create new chunk")
		return
	}

	chunk.Error = err
	chunk.Status = status
	chunk.Timestamp = time.Now()

	if !c.Write(chunk) {
		chunk.Reset()
	}
	atomic.StoreInt32(&c.state, stateClosed)
}
