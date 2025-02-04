package stream

import (
	"container/ring"
	"context"
	"errors"
	"fmt"
	"io"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/proxy/loadbalancer"
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
	buffer       *ring.Ring
	mu           sync.RWMutex
	broadcast    chan struct{}
	clientCount  int32
	writerCtx    context.Context
	writerCancel context.CancelFunc
	writerChan   chan struct{}
	writerCtxMu  sync.Mutex
	writerActive atomic.Bool

	lastError atomic.Value
	logger    logger.Logger
	config    *StreamConfig
	cm        *store.ConcurrencyManager
	streamID  string

	// state now represents our three‐state (active, draining, closed) machine.
	state int32

	lbResultOnWrite atomic.Pointer[loadbalancer.LoadBalancerResult]

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

func NewStreamCoordinator(streamID string, config *StreamConfig, cm *store.ConcurrencyManager, logger logger.Logger) *StreamCoordinator {
	logger.Debug("Initializing new StreamCoordinator")

	r := ring.New(config.SharedBufferSize)
	for i := 0; i < config.SharedBufferSize; i++ {
		r.Value = newChunkData()
		r = r.Next()
	}

	coord := &StreamCoordinator{
		buffer:     r,
		writerChan: make(chan struct{}, 1),
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
	if state != stateActive {
		c.logger.Warn("Attempt to register client on non-active stream")
		switch state {
		case stateDraining:
			return ErrStreamDraining
		default:
			return ErrStreamClosed
		}
	}

	count := atomic.AddInt32(&c.clientCount, 1)
	c.logger.Debugf("Client registered. Total clients: %d", count)
	return nil
}

func (c *StreamCoordinator) UnregisterClient() {
	count := atomic.AddInt32(&c.clientCount, -1)
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

	c.writerCtxMu.Lock()
	if c.writerCancel != nil {
		c.writerCancel()
		c.writerCancel = nil
	}
	c.writerCtxMu.Unlock()

	c.writerActive.Store(false)
	c.clearBuffer()
	atomic.StoreInt32(&c.state, stateClosed)
	c.notifySubscribers()
}

func (c *StreamCoordinator) HasClient() bool {
	count := atomic.LoadInt32(&c.clientCount)
	return count > 0
}

func (c *StreamCoordinator) GetWriterLBResult() *loadbalancer.LoadBalancerResult {
	return c.lbResultOnWrite.Load()
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

func (c *StreamCoordinator) StartWriter(ctx context.Context, lbResult *loadbalancer.LoadBalancerResult) {
	defer func() {
		c.lbResultOnWrite.Store(nil)
		if r := recover(); r != nil {
			c.logger.Errorf("Panic in StartWriter: %v", r)
			c.writeError(fmt.Errorf("internal server error"), proxy.StatusServerError)
		}
	}()
	defer lbResult.Response.Body.Close()

	if !c.writerActive.CompareAndSwap(false, true) {
		c.logger.Warn("Writer already active, aborting start")
		return
	}
	defer c.writerActive.Store(false)

	c.lbResultOnWrite.Store(lbResult)
	c.logger.Debug("StartWriter: Beginning read loop")

	buffer := make([]byte, c.config.ChunkSize)
	timeout := c.getTimeoutDuration()
	backoff := proxy.NewBackoffStrategy(c.config.InitialBackoff,
		time.Duration(c.config.TimeoutSeconds-1)*time.Second)

	c.cm.UpdateConcurrency(lbResult.Index, true)
	defer c.cm.UpdateConcurrency(lbResult.Index, false)

	lastSuccess := time.Now()
	lastErr := time.Now()
	zeroReads := 0

	for atomic.LoadInt32(&c.state) == stateActive {
		select {
		case <-ctx.Done():
			c.logger.Debug("StartWriter: Context cancelled")
			c.writeError(ctx.Err(), proxy.StatusClientClosed)
			return
		default:
			if c.shouldTimeout(lastSuccess, timeout) {
				c.writeError(nil, proxy.StatusServerError)
				return
			}

			n, err := lbResult.Response.Body.Read(buffer)
			c.logger.Debugf("StartWriter: Read %d bytes, err: %v", n, err)

			if n == 0 {
				zeroReads++
				if zeroReads > 10 {
					c.writeError(io.EOF, proxy.StatusEOF)
					return
				}
				time.Sleep(10 * time.Millisecond)
				continue
			}

			lastSuccess = time.Now()
			zeroReads = 0

			if err == io.EOF {
				if n > 0 {
					chunk := newChunkData()
					_, _ = chunk.Buffer.Write(buffer[:n])
					chunk.Timestamp = time.Now()
					if !c.Write(chunk) {
						chunk.Reset()
					}
				}
				c.writeError(io.EOF, proxy.StatusEOF)
				return
			}

			if err != nil {
				if c.shouldRetry(timeout) {
					backoff.Sleep(ctx)
					lastErr = time.Now()
					continue
				}
				c.writeError(err, proxy.StatusServerError)
				return
			}

			chunk := newChunkData()
			_, _ = chunk.Buffer.Write(buffer[:n])
			chunk.Timestamp = time.Now()
			if !c.Write(chunk) {
				chunk.Reset()
			}

			// Only reset the backoff if at least one second has passed
			if time.Since(lastErr) >= time.Second {
				backoff.Reset()
				lastErr = time.Now()
			}
		}
	}
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

	current, ok := c.buffer.Value.(*ChunkData)
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

	c.buffer = c.buffer.Next()
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
		fromPosition = c.buffer
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
			return nil, errorChunk, c.buffer
		}
	}

	// If we've caught up with the writer and the stream is active, wait.
	for fromPosition == c.buffer && atomic.LoadInt32(&c.state) == stateActive {
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
	for current != c.buffer {
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
		// In a well‐formed ring, current will eventually equal c.buffer.
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

	current := c.buffer
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
