package stream

import (
	"container/ring"
	"context"
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

var chunkPool = sync.Pool{
	New: func() interface{} {
		return &ChunkData{
			Buffer: bytebufferpool.Get(),
		}
	},
}

type StreamCoordinator struct {
	buffer      *ring.Ring
	mu          sync.RWMutex
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
		r.Value = chunkPool.Get()
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

	for c.active.Load() {
		select {
		case <-ctx.Done():
			c.writeError(ctx.Err(), proxy.StatusClientClosed)
			return
		case <-c.writerChan:
			c.writeError(io.EOF, proxy.StatusEOF)
			return
		default:
			if c.shouldTimeout(start, timeout) {
				c.writeError(nil, proxy.StatusServerError)
				return
			}

			n, err := resp.Body.Read(buffer)

			if err == io.EOF {
				if n > 0 {
					c.writeData(buffer[:n])
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
			c.writeData(buffer[:n])

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

	if !c.active.Load() {
		return false
	}

	// Get a new chunk from the pool
	newChunk := chunkPool.Get().(*ChunkData)

	// Reset the chunk
	newChunk.Status = chunk.Status
	newChunk.Error = chunk.Error
	newChunk.Timestamp = chunk.Timestamp

	// Only copy data if there is any
	if chunk.Buffer != nil && chunk.Buffer.Len() > 0 {
		newChunk.Buffer.Reset()
		_, err := newChunk.Buffer.Write(chunk.Buffer.Bytes())
		if err != nil {
			c.logger.Errorf("Error copying buffer: %v", err)
		}
	}

	// Return current chunk to pool if it exists
	if current := c.buffer.Value.(*ChunkData); current != nil {
		current.Buffer.Reset()
		bytebufferpool.Put(current.Buffer)
		chunkPool.Put(current)
	}

	c.buffer.Value = newChunk
	c.buffer = c.buffer.Next()

	if chunk.Error != nil || chunk.Status != 0 {
		c.lastError.Store(newChunk)
		c.active.Store(false)
	}

	c.dataNotify.Broadcast()
	return true
}

func (c *StreamCoordinator) ReadChunks(fromPosition *ring.Ring) ([]*ChunkData, *ChunkData, *ring.Ring) {
	c.dataNotify.L.Lock()
	defer c.dataNotify.L.Unlock()

	for fromPosition == c.buffer && c.active.Load() {
		c.dataNotify.Wait()
	}

	if err := c.lastError.Load().(*ChunkData); err != nil {
		return nil, err, fromPosition
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	chunks := make([]*ChunkData, 0, 32)
	current := fromPosition

	for current != c.buffer {
		if chunk, ok := current.Value.(*ChunkData); ok {
			if (chunk.Buffer != nil && chunk.Buffer.Len() > 0) || chunk.Error != nil || chunk.Status != 0 {
				newChunk := &ChunkData{
					Status:    chunk.Status,
					Error:     chunk.Error,
					Timestamp: chunk.Timestamp,
					Buffer:    bytebufferpool.Get(),
				}
				if chunk.Buffer != nil && chunk.Buffer.Len() > 0 {
					_, err := newChunk.Buffer.Write(chunk.Buffer.Bytes())
					if err != nil {
						c.logger.Errorf("Error copying buffer in ReadChunks: %v", err)
					}
				}
				chunks = append(chunks, newChunk)
			}
		}
		current = current.Next()
	}

	return chunks, nil, current
}

func (c *StreamCoordinator) clearBuffer() {
	c.mu.Lock()
	defer c.mu.Unlock()

	current := c.buffer
	for i := 0; i < c.config.SharedBufferSize; i++ {
		if chunk, ok := current.Value.(*ChunkData); ok {
			if chunk.Buffer != nil {
				chunk.Buffer.Reset()
				bytebufferpool.Put(chunk.Buffer)
			}
			chunkPool.Put(chunk)
		}
		current.Value = chunkPool.Get()
		current = current.Next()
	}
}

func (c *StreamCoordinator) getTimeoutDuration() time.Duration {
	if c.config.TimeoutSeconds == 0 {
		return time.Minute
	}
	return time.Duration(c.config.TimeoutSeconds) * time.Second
}

func (c *StreamCoordinator) writeData(data []byte) {
	chunk := chunkPool.Get().(*ChunkData)
	chunk.Buffer.Reset()
	_, err := chunk.Buffer.Write(data)
	if err != nil {
		c.logger.Errorf("Error writing to buffer: %v", err)
	}
	chunk.Timestamp = time.Now()
	c.Write(chunk)
}

func (c *StreamCoordinator) writeError(err error, status int) {
	chunk := chunkPool.Get().(*ChunkData)
	chunk.Error = err
	chunk.Status = status
	chunk.Timestamp = time.Now()
	c.Write(chunk)
}
