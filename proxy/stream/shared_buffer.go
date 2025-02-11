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

// ChunkData holds a chunk of streamed data along with metadata.
type ChunkData struct {
	Buffer    *bytebufferpool.ByteBuffer
	Error     error
	Status    int
	Timestamp time.Time

	seq int64 // unexported sequence number for internal tracking.
}

// newChunkData creates a new chunk with a fresh ByteBuffer.
func newChunkData() *ChunkData {
	return &ChunkData{
		Buffer: bytebufferpool.Get(),
		seq:    0,
	}
}

// Reset resets the chunk. It returns the underlying buffer
// to the pool, obtains a new one, and clears all metadata.
// Once Reset is called the caller must not use the old buffer.
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

// Internal state constants.
const (
	stateActive int32 = iota
	stateDraining
	stateClosed
)

// StreamCoordinator coordinates the ring-buffer used for streaming.
type StreamCoordinator struct {
	buffer       *ring.Ring
	mu           sync.RWMutex
	broadcast    chan struct{}
	clientCount  int32
	writerCtx    context.Context
	writerCancel context.CancelFunc
	writerChan   chan struct{}
	writerCtxMu  sync.Mutex

	lastError atomic.Value
	logger    logger.Logger
	config    *StreamConfig
	cm        *store.ConcurrencyManager
	streamID  string

	initializationMu sync.Mutex

	// state represents active, draining, or closed.
	state int32

	lbResultOnWrite atomic.Pointer[loadbalancer.LoadBalancerResult]

	// writeSeq is an atomic counter to track the order of chunks.
	writeSeq int64
}

// subscribe returns the current broadcast channel.
func (c *StreamCoordinator) subscribe() <-chan struct{} {
	c.mu.RLock()
	ch := c.broadcast
	c.mu.RUnlock()
	return ch
}

// notifySubscribers closes the current broadcast channel and
// creates a new one so waiting clients can be notified.
func (c *StreamCoordinator) notifySubscribers() {
	c.mu.Lock()
	close(c.broadcast)
	c.broadcast = make(chan struct{})
	c.mu.Unlock()
}

// NewStreamCoordinator initializes a new StreamCoordinator.
func NewStreamCoordinator(
	streamID string,
	config *StreamConfig,
	cm *store.ConcurrencyManager,
	logger logger.Logger,
) *StreamCoordinator {
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

// GetWriterLBResult returns the load balancer result for the current writer call.
func (c *StreamCoordinator) GetWriterLBResult() *loadbalancer.LoadBalancerResult {
	return c.lbResultOnWrite.Load()
}

// RegisterClient registers a new client and returns an error if the stream
// is no longer active.
func (c *StreamCoordinator) RegisterClient() error {
	if atomic.LoadInt32(&c.state) != stateActive {
		c.logger.Warn("Attempt to register a client on a non-active stream")
		return errors.New("stream is closed")
	}
	count := atomic.AddInt32(&c.clientCount, 1)
	c.logger.Logf("Client registered (%s). Total clients: %d", c.streamID, count)
	return nil
}

// UnregisterClient unregisters a client and cleans up resources if it was the last.
func (c *StreamCoordinator) UnregisterClient() {
	count := atomic.AddInt32(&c.clientCount, -1)
	c.logger.Logf("Client unregistered (%s). Remaining clients: %d", c.streamID, count)
	if count == 0 {
		c.logger.Log("Last client unregistered, cleaning up resources")
		atomic.StoreInt32(&c.state, stateDraining)
		// Signal the writer to shut down.
		select {
		case c.writerChan <- struct{}{}:
			c.logger.Debug("Sent shutdown signal to writer")
		default:
			c.logger.Debug("Writer channel already has shutdown signal")
		}
		c.clearBuffer()
		c.notifySubscribers()
	}
}

// HasClient returns true if there is at least one client connected.
func (c *StreamCoordinator) HasClient() bool {
	return atomic.LoadInt32(&c.clientCount) > 0
}

// shouldTimeout checks if the time since the last successful read exceeds the timeout.
func (c *StreamCoordinator) shouldTimeout(lastSuccess time.Time, timeout time.Duration) bool {
	shouldTimeout := c.config.TimeoutSeconds > 0 && time.Since(lastSuccess) >= timeout
	if shouldTimeout {
		c.logger.Debugf("Stream timed out after %v", time.Since(lastSuccess))
	}
	return shouldTimeout
}

// shouldRetry indicates whether the writer should retry reading on error.
func (c *StreamCoordinator) shouldRetry(timeout time.Duration) bool {
	return c.config.TimeoutSeconds == 0 || timeout > 0
}

// StartWriter reads from the upstream response and writes chunks into the ring-buffer.
func (c *StreamCoordinator) StartWriter(
	ctx context.Context,
	lbResult *loadbalancer.LoadBalancerResult,
) {
	defer func() {
		c.lbResultOnWrite.Store(nil)
		if r := recover(); r != nil {
			c.logger.Errorf("Panic in StartWriter: %v", r)
			c.writeError(fmt.Errorf("internal server error"), proxy.StatusServerError)
		}
	}()
	defer lbResult.Response.Body.Close()

	c.lbResultOnWrite.Store(lbResult)
	c.logger.Debug("StartWriter: Beginning read loop")

	buffer := make([]byte, c.config.ChunkSize)
	timeout := c.getTimeoutDuration()
	backoff := proxy.NewBackoffStrategy(
		c.config.InitialBackoff,
		time.Duration(c.config.TimeoutSeconds-1)*time.Second,
	)

	c.cm.UpdateConcurrency(lbResult.Index, true)
	defer c.cm.UpdateConcurrency(lbResult.Index, false)

	lastSuccess := time.Now()
	lastErr := time.Now()
	zeroReads := 0
	done := make(chan struct{}, 1)
	defer close(done)

	go func() {
		select {
		case <-ctx.Done():
			atomic.StoreInt32(&c.state, stateClosed)
			c.notifySubscribers()
		case <-done:
		}
	}()

	tempChunk := newChunkData()
	defer tempChunk.Reset()

	for atomic.LoadInt32(&c.state) == stateActive {
		select {
		case <-ctx.Done():
			c.logger.Debug("StartWriter: Context cancelled")
			c.writeError(ctx.Err(), proxy.StatusClientClosed)
			return
		case <-c.writerChan:
			c.logger.Debug("StartWriter: Received shutdown signal")
			c.writeError(io.EOF, proxy.StatusEOF)
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
					tempChunk.Reset()
					_, _ = tempChunk.Buffer.Write(buffer[:n])
					tempChunk.Timestamp = time.Now()
					c.Write(tempChunk)
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

			tempChunk.Reset()
			_, _ = tempChunk.Buffer.Write(buffer[:n])
			tempChunk.Timestamp = time.Now()
			c.Write(tempChunk)

			if time.Since(lastErr) >= time.Second {
				backoff.Reset()
				lastErr = time.Now()
			}
		}
	}
}

// Write performs a zero‑copy write via a buffer swap.
// Regardless of success or failure, the provided chunk is immediately reset,
// transferring full buffer ownership to the coordinator and returning the old
// buffer back to the pool. This prevents any leaks or accidental reuse.
func (c *StreamCoordinator) Write(chunk *ChunkData) bool {
	if chunk == nil {
		c.logger.Debug("Write: Received nil chunk")
		return false
	}

	c.mu.Lock()
	// If the stream isn't active, we still must consume (reset) the chunk.
	if atomic.LoadInt32(&c.state) != stateActive {
		c.logger.Debug("Write: Stream not active")
		c.mu.Unlock()
		chunk.Reset()
		return false
	}

	current, ok := c.buffer.Value.(*ChunkData)
	if !ok || current == nil {
		c.logger.Debug("Write: Current buffer position is nil")
		c.mu.Unlock()
		chunk.Reset()
		return false
	}

	// Increment and assign a sequence number.
	current.seq = atomic.AddInt64(&c.writeSeq, 1)

	// Perform the swap:
	// - The ring's current chunk now receives the data from the caller's chunk.
	// - The caller's chunk is given the ring's old buffer.
	oldBuffer := current.Buffer
	current.Buffer = chunk.Buffer
	chunk.Buffer = oldBuffer

	current.Error = chunk.Error
	current.Status = chunk.Status
	current.Timestamp = chunk.Timestamp

	// Advance the ring pointer.
	c.buffer = c.buffer.Next()
	c.logger.Debug("Write: Advanced buffer position")

	// Mark error state if needed.
	if current.Error != nil || current.Status != 0 {
		if c.lastError.Load() == nil {
			c.lastError.Store(current)
		}
		atomic.StoreInt32(&c.state, stateDraining)
		c.logger.Debugf("Write: Setting error state: err=%v, status=%d", current.Error, current.Status)
	}
	c.mu.Unlock()

	// Notify waiting subscribers.
	c.notifySubscribers()
	// Enforce the new ownership rule:
	// Immediately reset the provided chunk so that its swapped-out buffer is
	// returned to the pool and the caller does not continue using stale data.
	chunk.Reset()

	return true
}

// ReadChunks retrieves chunks from the ring for a client, given a starting position.
func (c *StreamCoordinator) ReadChunks(fromPosition *ring.Ring) (
	[]*ChunkData, *ChunkData, *ring.Ring,
) {
	c.mu.RLock()
	if fromPosition == nil {
		c.logger.Debug("ReadChunks: fromPosition is nil, using current buffer")
		fromPosition = c.buffer
	}
	// Check if the client's pointer is too far behind.
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
			return nil, errorChunk, c.buffer
		}
	}

	// Wait if the client has caught up with the writer and the stream is active.
	for fromPosition == c.buffer && atomic.LoadInt32(&c.state) == stateActive {
		c.mu.RUnlock()
		ch := c.subscribe()
		<-ch
		c.mu.RLock()
	}

	chunks := make([]*ChunkData, 0, c.config.SharedBufferSize)
	current := fromPosition
	var errorFound bool
	var errorChunk *ChunkData

	// Iterate until we reach the writer's current position.
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
		if current == fromPosition {
			break
		}
	}
	c.mu.RUnlock()

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

// clearBuffer resets every chunk in the ring.
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

// getTimeoutDuration returns the streaming timeout duration.
func (c *StreamCoordinator) getTimeoutDuration() time.Duration {
	if c.config.TimeoutSeconds == 0 {
		return time.Minute
	}
	return time.Duration(c.config.TimeoutSeconds) * time.Second
}

// writeError writes an error chunk to the stream, consuming the chunk.
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
