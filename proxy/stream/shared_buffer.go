package stream

import (
	"container/ring"
	"context"
	"fmt"
	"io"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/store"
	"net/http"
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
}

func newChunkData() *ChunkData {
	return &ChunkData{
		Buffer: bytebufferpool.Get(),
	}
}

func (c *ChunkData) Reset() {
	if c.Buffer != nil {
		c.Buffer.Reset()
		bytebufferpool.Put(c.Buffer)
	}
	c.Buffer = bytebufferpool.Get()
	c.Error = nil
	c.Status = 0
	c.Timestamp = time.Time{}
}

type StreamCoordinator struct {
	buffer      *ring.Ring
	mu          sync.Mutex
	clientCount int32
	writerChan  chan struct{}
	lastError   atomic.Value
	logger      logger.Logger
	config      *StreamConfig
	cm          *store.ConcurrencyManager
	streamID    string
	dataNotify  *sync.Cond
	active      atomic.Bool
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
		dataNotify: sync.NewCond(&sync.Mutex{}),
	}
	coord.active.Store(true)
	coord.lastError.Store((*ChunkData)(nil))
	coord.dataNotify = sync.NewCond(&coord.mu)

	logger.Debugf("StreamCoordinator initialized with buffer size: %d, chunk size: %d",
		config.SharedBufferSize, config.ChunkSize)
	return coord
}

func (c *StreamCoordinator) RegisterClient() {
	count := atomic.AddInt32(&c.clientCount, 1)
	c.logger.Logf("Client registered (%s). Total clients: %d", c.streamID, count)
}

func (c *StreamCoordinator) UnregisterClient() {
	count := atomic.AddInt32(&c.clientCount, -1)
	c.logger.Logf("Client unregistered (%s). Remaining clients: %d", c.streamID, count)

	if count == 0 {
		c.logger.Log("Last client unregistered, cleaning up resources")
		select {
		case c.writerChan <- struct{}{}:
			c.logger.Debug("Sent shutdown signal to writer")
		default:
			c.logger.Debug("Writer channel already has shutdown signal")
		}
		c.clearBuffer()
	}
}

func (c *StreamCoordinator) HasClient() bool {
	count := atomic.LoadInt32(&c.clientCount)

	return count > 0
}

func (c *StreamCoordinator) shouldTimeout(timeStarted time.Time, timeout time.Duration) bool {
	shouldTimeout := c.config.TimeoutSeconds > 0 && time.Since(timeStarted) >= timeout
	if shouldTimeout {
		c.logger.Debugf("Stream timed out after %v", time.Since(timeStarted))
	}
	return shouldTimeout
}

func (c *StreamCoordinator) shouldRetry(timeout time.Duration) bool {
	return c.config.TimeoutSeconds == 0 || timeout > 0
}

func (c *StreamCoordinator) shouldResetTimer(lastErr, timeStarted time.Time) bool {
	return lastErr.Equal(timeStarted) || time.Since(lastErr) >= time.Second
}

func (c *StreamCoordinator) StartWriter(ctx context.Context, m3uIndex string, resp *http.Response) {
	defer func() {
		if r := recover(); r != nil {
			c.logger.Errorf("Panic in StartWriter: %v", r)
			c.writeError(fmt.Errorf("internal server error"), proxy.StatusServerError)
		}
	}()
	defer resp.Body.Close()
	c.logger.Debug("StartWriter: Beginning read loop")

	buffer := make([]byte, c.config.ChunkSize)
	timeout := c.getTimeoutDuration()
	backoff := proxy.NewBackoffStrategy(c.config.InitialBackoff,
		time.Duration(c.config.TimeoutSeconds-1)*time.Second)

	c.cm.UpdateConcurrency(m3uIndex, true)
	defer c.cm.UpdateConcurrency(m3uIndex, false)

	start := time.Now()
	lastErr := start
	zeroReads := 0

	done := make(chan struct{}, 1)
	defer close(done)

	go func() {
		select {
		case <-ctx.Done():
			c.active.Store(false)
			c.dataNotify.Broadcast()
		case <-done:
		}
	}()

	tempChunk := newChunkData()
	defer tempChunk.Reset()

	for c.active.Load() {
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
			if c.shouldTimeout(start, timeout) {
				c.writeError(nil, proxy.StatusServerError)
				return
			}

			n, err := resp.Body.Read(buffer)
			c.logger.Debugf("StartWriter: Read %d bytes, err: %v", n, err)

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

			if n == 0 {
				if zeroReads++; zeroReads > 10 {
					c.writeError(io.EOF, proxy.StatusEOF)
					return
				}
				time.Sleep(10 * time.Millisecond)
				continue
			}

			zeroReads = 0
			tempChunk.Reset()
			_, _ = tempChunk.Buffer.Write(buffer[:n])
			tempChunk.Timestamp = time.Now()
			c.Write(tempChunk)

			if c.shouldResetTimer(lastErr, start) {
				start = time.Now()
				backoff.Reset()
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
	defer c.mu.Unlock()

	if !c.active.Load() {
		c.logger.Debug("Write: Stream not active")
		return false
	}

	current := c.buffer.Value.(*ChunkData)
	if current == nil {
		c.logger.Debug("Write: Current buffer position is nil")
		return false
	}

	// Swap buffers to avoid copying
	oldBuffer := current.Buffer
	current.Buffer = chunk.Buffer
	chunk.Buffer = oldBuffer // Will be reset by caller

	current.Error = chunk.Error
	current.Status = chunk.Status
	current.Timestamp = chunk.Timestamp

	c.buffer = c.buffer.Next()
	c.logger.Debug("Write: Advanced buffer position")

	// Store error state but don't deactivate until next write
	if current.Error != nil || current.Status != 0 {
		c.lastError.Store(current)
		// Mark inactive for next write
		defer func() {
			c.active.Store(false)
			c.logger.Debugf("Write: Setting error state: err=%v, status=%d", current.Error, current.Status)
		}()
	}

	c.dataNotify.Broadcast()
	return true
}

func (c *StreamCoordinator) ReadChunks(fromPosition *ring.Ring) ([]*ChunkData, *ChunkData, *ring.Ring) {
	if fromPosition == nil {
		c.logger.Debug("ReadChunks: fromPosition is nil, using current buffer")
		fromPosition = c.buffer
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	chunks := make([]*ChunkData, 0, c.config.SharedBufferSize)
	current := fromPosition

	// If we've caught up with the writer and the stream is still active, wait
	for current == c.buffer && c.active.Load() {
		c.dataNotify.Wait()
	}

	// First read all available data chunks
	errorFound := false
	var errorChunk *ChunkData

	for current != nil && current != c.buffer {
		if chunk, ok := current.Value.(*ChunkData); ok && chunk != nil {
			if chunk.Buffer != nil && chunk.Buffer.Len() > 0 {
				// Create new chunk for data
				newChunk := &ChunkData{
					Buffer:    bytebufferpool.Get(),
					Error:     nil,
					Status:    0,
					Timestamp: chunk.Timestamp,
				}
				newChunk.Buffer.Write(chunk.Buffer.Bytes())
				chunks = append(chunks, newChunk)
			}

			// If this chunk has an error, store it but continue processing data
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
	}

	// After processing all data, handle any error state
	if errorFound && errorChunk != nil {
		return chunks, errorChunk, current
	}

	// If no error was found in chunks, check stored error state
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
}
