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
)

type ChunkData struct {
	Data      []byte
	Error     error
	Status    int
	Timestamp time.Time
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
}

func NewStreamCoordinator(streamID string, config *StreamConfig, cm *store.ConcurrencyManager, logger logger.Logger) *StreamCoordinator {
	logger.Debug("Initializing new StreamCoordinator")
	r := ring.New(config.SharedBufferSize)
	for i := 0; i < config.SharedBufferSize; i++ {
		r.Value = &ChunkData{Data: make([]byte, 0, config.ChunkSize)}
		r = r.Next()
	}

	coord := &StreamCoordinator{
		buffer:     r,
		writerChan: make(chan struct{}),
		logger:     logger,
		config:     config,
		cm:         cm,
		streamID:   streamID,
	}
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
	buffer := make([]byte, c.config.ChunkSize)
	timeout := c.getTimeoutDuration()
	backoff := proxy.NewBackoffStrategy(c.config.InitialBackoff,
		time.Duration(c.config.TimeoutSeconds-1)*time.Second)

	c.cm.UpdateConcurrency(m3uIndex, true)
	defer c.cm.UpdateConcurrency(m3uIndex, false)

	start := time.Now()
	lastErr := start
	zeroReads := 0

	c.logger.Logf("Starting shared buffer writer from M3U_%s for stream ID: %s", m3uIndex, c.streamID)
	defer c.logger.Logf("Stopped shared buffer writer from M3U_%s for stream ID: %s", m3uIndex, c.streamID)

	for {
		select {
		case <-ctx.Done():
			c.Write(&ChunkData{
				Error:     ctx.Err(),
				Status:    proxy.StatusClientClosed,
				Timestamp: time.Now(),
			})
			return

		case <-c.writerChan:
			c.Write(&ChunkData{
				Error:     io.EOF,
				Status:    proxy.StatusEOF,
				Timestamp: time.Now(),
			})
			return

		default:
			if c.shouldTimeout(start, timeout) {
				c.Write(&ChunkData{
					Status:    proxy.StatusServerError,
					Timestamp: time.Now(),
				})
				return
			}

			n, err := resp.Body.Read(buffer)
			c.logger.Debugf("StartWriter has received %d bytes (error: %v)", n, err)

			if err == io.EOF {
				if n > 0 {
					c.Write(&ChunkData{
						Data:      buffer[:n],
						Timestamp: time.Now(),
					})
				}
				c.Write(&ChunkData{
					Error:     io.EOF,
					Status:    proxy.StatusEOF,
					Timestamp: time.Now(),
				})
				return
			}

			if err != nil {
				if c.shouldRetry(timeout) {
					backoff.Sleep(ctx)
					lastErr = time.Now()
					continue
				}
				c.Write(&ChunkData{
					Error:     err,
					Status:    proxy.StatusServerError,
					Timestamp: time.Now(),
				})
				return
			}

			if n == 0 {
				time.Sleep(10 * time.Millisecond)
				if zeroReads++; zeroReads > 10 {
					c.Write(&ChunkData{
						Error:     io.EOF,
						Status:    proxy.StatusEOF,
						Timestamp: time.Now(),
					})
					return
				}
				continue
			}

			zeroReads = 0
			c.Write(&ChunkData{
				Data:      buffer[:n],
				Timestamp: time.Now(),
			})

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

	select {
	case <-c.writerChan:
		return false
	default:
	}

	if len(chunk.Data) > 0 {
		data := make([]byte, len(chunk.Data))
		copy(data, chunk.Data)
		chunk.Data = data
	}

	if chunk.Error != nil || chunk.Status != 0 {
		c.lastError.Store(chunk)
	}

	c.buffer.Value = chunk
	c.buffer = c.buffer.Next()
	return true
}

func (c *StreamCoordinator) ReadChunks(fromPosition *ring.Ring) ([]*ChunkData, *ChunkData, *ring.Ring) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.lastError.Load().(*ChunkData); err != nil {
		return nil, err, fromPosition
	}

	chunks := make([]*ChunkData, 0, c.config.SharedBufferSize)
	current := fromPosition

	for current != c.buffer {
		if chunk, ok := current.Value.(*ChunkData); ok &&
			(len(chunk.Data) > 0 || chunk.Error != nil || chunk.Status != 0) {
			chunks = append(chunks, chunk)
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
		current.Value = &ChunkData{Data: make([]byte, 0, c.config.ChunkSize)}
		current = current.Next()
	}
}

func (c *StreamCoordinator) getTimeoutDuration() time.Duration {
	if c.config.TimeoutSeconds == 0 {
		return time.Minute
	}
	return time.Duration(c.config.TimeoutSeconds) * time.Second
}
