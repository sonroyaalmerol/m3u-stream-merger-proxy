package stream

import (
	"container/ring"
	"context"
	"io"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/utils"
	"net/http"
	"sync/atomic"
	"time"
)

type StreamHandler struct {
	config      *StreamConfig
	logger      logger.Logger
	coordinator *StreamCoordinator
}

func NewStreamHandler(config *StreamConfig, coordinator *StreamCoordinator, logger logger.Logger) *StreamHandler {
	return &StreamHandler{
		config:      config,
		logger:      logger,
		coordinator: coordinator,
	}
}

type StreamResult struct {
	BytesWritten int64
	Error        error
	Status       int
}

func (h *StreamHandler) HandleStream(
	ctx context.Context,
	m3uIndex string,
	resp *http.Response,
	writer ResponseWriter,
	remoteAddr string,
) StreamResult {
	// If EOF is expected, bypass shared buffer and stream directly
	if utils.EOFIsExpected(resp) {
		return h.handleDirectStream(ctx, resp, writer, remoteAddr)
	}

	return h.handleBufferedStream(ctx, m3uIndex, resp, writer, remoteAddr)
}

func (h *StreamHandler) handleDirectStream(
	ctx context.Context,
	resp *http.Response,
	writer ResponseWriter,
	remoteAddr string,
) StreamResult {
	buffer := h.createBuffer()
	defer func() {
		buffer = nil
		resp.Body.Close()
	}()

	timeoutDuration := h.getTimeoutDuration()
	backoff := proxy.NewBackoffStrategy(h.config.InitialBackoff, time.Duration(h.config.TimeoutSeconds-1)*time.Second)

	timeStarted := time.Now()
	lastErr := timeStarted
	var bytesWritten int64

	for {
		select {
		case <-ctx.Done():
			h.logger.Debugf("Client disconnected from stream: %s", remoteAddr)
			return StreamResult{bytesWritten, ctx.Err(), proxy.StatusClientClosed}

		default:
			result := h.readChunk(ctx, resp, buffer)

			if h.coordinator.shouldTimeout(timeStarted, timeoutDuration) {
				h.logger.Errorf("Timeout reached while trying to stream: %s", remoteAddr)
				return StreamResult{bytesWritten, nil, proxy.StatusServerError}
			}

			switch {
			case result.Error == context.Canceled:
				h.logger.Debugf("Client disconnected while reading chunk: %s", remoteAddr)
				return StreamResult{bytesWritten, result.Error, proxy.StatusClientClosed}

			case result.Error == io.EOF:
				if result.N > 0 {
					written, err := writer.Write(buffer[:result.N])
					if err != nil {
						h.logger.Errorf("Error writing final buffer: %s", err.Error())
						return StreamResult{bytesWritten, err, 0}
					}
					bytesWritten += int64(written)
					if flusher, ok := writer.(StreamFlusher); ok {
						flusher.Flush()
					}
				}
				return h.handleEOF(resp, remoteAddr, bytesWritten)

			case result.Error != nil:
				if h.coordinator.shouldRetry(timeoutDuration) {
					h.logger.Errorf("Error reading stream: %s", result.Error.Error())
					backoff.Sleep(ctx)
					lastErr = time.Now()
					continue
				}
				return StreamResult{bytesWritten, result.Error, proxy.StatusServerError}

			default:
				written, err := writer.Write(buffer[:result.N])
				if err != nil {
					h.logger.Errorf("Error writing to response: %s", err.Error())
					return StreamResult{bytesWritten, err, 0}
				}
				bytesWritten += int64(written)
				if flusher, ok := writer.(StreamFlusher); ok {
					flusher.Flush()
				}

				if h.coordinator.shouldResetTimer(lastErr, timeStarted) {
					timeStarted = time.Now()
					backoff.Reset()
				}
			}
		}
	}
}

func (h *StreamHandler) handleBufferedStream(
	ctx context.Context,
	m3uIndex string,
	resp *http.Response,
	writer ResponseWriter,
	remoteAddr string,
) StreamResult {
	h.coordinator.RegisterClient()
	defer h.coordinator.UnregisterClient()
	if atomic.LoadInt32(&h.coordinator.clientCount) == 1 {
		h.logger.Debugf("Starting writer goroutine for m3uIndex: %s", m3uIndex)
		go h.coordinator.StartWriter(ctx, m3uIndex, resp)
	}
	var bytesWritten int64
	var lastPosition *ring.Ring

	h.coordinator.mu.RLock()
	lastPosition = h.coordinator.buffer
	h.logger.Debugf("Initial buffer position set: %p", lastPosition)
	h.coordinator.mu.RUnlock()

	for {
		select {
		case <-ctx.Done():
			h.logger.Debugf("Context done, bytes written: %d", bytesWritten)
			return StreamResult{bytesWritten, ctx.Err(), proxy.StatusClientClosed}
		default:
			h.coordinator.mu.RLock()
			if errChunk := h.coordinator.lastError.Load().(*ChunkData); errChunk != nil {
				h.logger.Debugf("Found error chunk: error=%v, status=%d", errChunk.Error, errChunk.Status)
				h.coordinator.mu.RUnlock()

				currentPos := lastPosition
				h.logger.Debugf("Processing remaining chunks before error, starting from: %p", currentPos)
				for currentPos != h.coordinator.buffer {
					if chunk, ok := currentPos.Value.(*ChunkData); ok {
						h.logger.Debugf("Found chunk: len(Data)=%d, Error=%v, Status=%d", len(chunk.Data), chunk.Error, chunk.Status)
						if len(chunk.Data) > 0 {
							written, err := writer.Write(chunk.Data)
							if err != nil {
								h.logger.Errorf("Error writing remaining chunks: %s", err.Error())
								return StreamResult{bytesWritten, err, 0}
							}
							bytesWritten += int64(written)
							h.logger.Debugf("Wrote %d bytes, total written: %d", written, bytesWritten)
							if flusher, ok := writer.(StreamFlusher); ok {
								flusher.Flush()
							}
						}
					}
					currentPos = currentPos.Next()
					h.logger.Debugf("Moving to next position: %p", currentPos)
				}
				return StreamResult{bytesWritten, errChunk.Error, errChunk.Status}
			}

			hasNewData := false
			h.logger.Debugf("Processing new chunks from position: %p", lastPosition)
			for lastPosition != h.coordinator.buffer {
				if chunk, ok := lastPosition.Value.(*ChunkData); ok {
					h.logger.Debugf("Processing chunk: len(Data)=%d, Error=%v, Status=%d", len(chunk.Data), chunk.Error, chunk.Status)
					if len(chunk.Data) > 0 {
						hasNewData = true
						written, err := writer.Write(chunk.Data)
						if err != nil {
							h.coordinator.mu.RUnlock()
							h.logger.Errorf("Error writing to client: %s", err.Error())
							return StreamResult{bytesWritten, err, 0}
						}
						bytesWritten += int64(written)
						h.logger.Debugf("Wrote %d bytes, total written: %d", written, bytesWritten)
						if flusher, ok := writer.(StreamFlusher); ok {
							flusher.Flush()
						}
					}
				}
				lastPosition = lastPosition.Next()
				h.logger.Debugf("Moving to next position: %p", lastPosition)
			}

			h.coordinator.mu.RUnlock()
			if !hasNewData {
				h.logger.Debugf("No new data found, sleeping")
				time.Sleep(10 * time.Millisecond)
			}
		}
	}
}

func (h *StreamHandler) handleEOF(
	resp *http.Response,
	remoteAddr string,
	bytesWritten int64,
) StreamResult {
	if utils.EOFIsExpected(resp) || h.config.TimeoutSeconds == 0 {
		h.logger.Debugf("Stream ended (expected EOF reached): %s", remoteAddr)
		return StreamResult{bytesWritten, nil, proxy.StatusEOF}
	}

	h.logger.Errorf("Stream ended (unexpected EOF reached): %s", remoteAddr)
	return StreamResult{bytesWritten, io.EOF, proxy.StatusEOF}
}

func (h *StreamHandler) createBuffer() []byte {
	if h.config.ChunkSize > 0 {
		return make([]byte, h.config.ChunkSize)
	}
	return make([]byte, 1024)
}

func (h *StreamHandler) getTimeoutDuration() time.Duration {
	if h.config.TimeoutSeconds == 0 {
		return time.Minute
	}
	return time.Duration(h.config.TimeoutSeconds) * time.Second
}

type ReadResult struct {
	N     int
	Error error
}

func (h *StreamHandler) readChunk(
	ctx context.Context,
	resp *http.Response,
	buffer []byte,
) ReadResult {
	readChan := make(chan ReadResult, 1)
	reader := resp.Body

	go func() {
		n, err := reader.Read(buffer)
		readChan <- ReadResult{n, err}
	}()

	select {
	case <-ctx.Done():
		h.logger.Error("Context canceled for stream")
		reader.Close()
		return ReadResult{0, ctx.Err()}
	case result := <-readChan:
		return result
	}
}
