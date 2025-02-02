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
	buffer          *ring.Ring
	mu              sync.Mutex
	clientCount     int32
	writerChan      chan struct{}
	lastError       atomic.Value
	logger          logger.Logger
	config          *StreamConfig
	cm              *store.ConcurrencyManager
	streamID        string
	dataNotify      *sync.Cond
	active          atomic.Bool
	initialBuffered sync.Map
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
		c.initialBuffered = sync.Map{} // Reset the buffering state
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
	c.mu.Lock()
	defer c.mu.Unlock()

	c.logger.Debugf("Write: Starting write operation. Active=%v, ChunkSize=%d",
		c.active.Load(), chunk.Buffer.Len())

	if !c.active.Load() {
		c.logger.Debug("Write: Stream not active, skipping write")
		return false
	}

	// Get the current chunk and reset it
	current := c.buffer.Value.(*ChunkData)
	current.Reset()

	// Copy data if there is any
	if chunk.Buffer != nil && chunk.Buffer.Len() > 0 {
		n, err := current.Buffer.Write(chunk.Buffer.B)
		c.logger.Debugf("Write: Copied %d bytes to buffer, err=%v", n, err)
		if err != nil {
			c.logger.Errorf("Error copying buffer: %v", err)
		}
	}

	current.Status = chunk.Status
	current.Error = chunk.Error
	current.Timestamp = chunk.Timestamp

	c.buffer = c.buffer.Next()
	c.logger.Debug("Write: Advanced buffer position")

	if chunk.Error != nil || chunk.Status != 0 {
		c.lastError.Store(current)
		c.active.Store(false)
		c.logger.Debugf("Write: Setting error state: err=%v, status=%d", chunk.Error, chunk.Status)
	}

	c.dataNotify.Broadcast()
	return true
}

func (c *StreamCoordinator) ReadChunks(fromPosition *ring.Ring) ([]*ChunkData, *ChunkData, *ring.Ring) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.logger.Debugf("ReadChunks: Starting read. Active=%v", c.active.Load())

	// Check if this is a new client position
	clientKey := fmt.Sprintf("%p", fromPosition)
	if _, exists := c.initialBuffered.Load(clientKey); !exists {
		c.initialBuffered.Store(clientKey, false)

		// Calculate required buffer fill
		requiredChunks := int(float64(c.config.SharedBufferSize) * 0.3)

		// Count available chunks
		current := fromPosition
		availableChunks := 0
		for current != c.buffer {
			if chunk, ok := current.Value.(*ChunkData); ok {
				if chunk.Buffer != nil && chunk.Buffer.Len() > 0 {
					availableChunks++
				}
			}
			current = current.Next()
		}

		// Wait until we have enough chunks or stream becomes inactive
		for availableChunks < requiredChunks && c.active.Load() {
			c.logger.Debugf("ReadChunks: Waiting for initial buffer. Have %d/%d chunks",
				availableChunks, requiredChunks)
			c.dataNotify.Wait()

			// Recount available chunks
			availableChunks = 0
			current = fromPosition
			for current != c.buffer {
				if chunk, ok := current.Value.(*ChunkData); ok {
					if chunk.Buffer != nil && chunk.Buffer.Len() > 0 {
						availableChunks++
					}
				}
				current = current.Next()
			}
		}

		c.initialBuffered.Store(clientKey, true)
		c.logger.Debug("ReadChunks: Initial buffer threshold met")
	}

	// Regular waiting for new data
	for fromPosition == c.buffer && c.active.Load() {
		c.logger.Debug("ReadChunks: Waiting for new data")
		c.dataNotify.Wait()
		c.logger.Debug("ReadChunks: Woke up from wait")
	}

	chunks := make([]*ChunkData, 0, 32)
	current := fromPosition

	// Process available chunks
	bufferCount := 0
	for current != c.buffer {
		if chunk, ok := current.Value.(*ChunkData); ok {
			bufferCount++
			if (chunk.Buffer != nil && chunk.Buffer.Len() > 0) || chunk.Error != nil || chunk.Status != 0 {
				newChunk := &ChunkData{
					Status:    chunk.Status,
					Error:     chunk.Error,
					Timestamp: chunk.Timestamp,
					Buffer:    bytebufferpool.Get(),
				}
				if chunk.Buffer != nil && chunk.Buffer.Len() > 0 {
					_, _ = newChunk.Buffer.Write(chunk.Buffer.Bytes())
					c.logger.Debugf("ReadChunks: Found chunk with %d bytes", chunk.Buffer.Len())
				}
				chunks = append(chunks, newChunk)
			}
		}
		current = current.Next()
	}

	c.logger.Debugf("ReadChunks: Processed %d positions, found %d chunks with data",
		bufferCount, len(chunks))

	// Check for error after processing chunks
	if err := c.lastError.Load().(*ChunkData); err != nil {
		c.logger.Debugf("ReadChunks: Found error after processing chunks: %v", err.Error)
		return chunks, err, current
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
	chunk.Error = err
	chunk.Status = status
	chunk.Timestamp = time.Now()
	c.Write(chunk)
	chunk.Reset() // Reset after writing
}
