package stream

import (
	"context"
	"fmt"
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
	if resp != nil && utils.EOFIsExpected(resp) {
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
	if h.coordinator == nil {
		h.logger.Error("handleBufferedStream: coordinator is nil")
		return StreamResult{0, fmt.Errorf("coordinator is nil"), proxy.StatusServerError}
	}

	h.coordinator.RegisterClient()
	defer h.coordinator.UnregisterClient()

	if atomic.LoadInt32(&h.coordinator.clientCount) == 1 {
		go h.coordinator.StartWriter(ctx, m3uIndex, resp)
	}

	var bytesWritten int64
	lastPosition := h.coordinator.buffer.Prev() // Start from previous to get first new chunk

	// Create a channel to signal writer goroutine to stop
	done := make(chan struct{})
	defer close(done)

	// Create a context with cancel for the writer goroutine
	writerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Handle context cancellation in a separate goroutine
	go func() {
		select {
		case <-ctx.Done():
			h.logger.Debugf("Client disconnected: %s", remoteAddr)
			cancel()
		case <-done:
			return
		}
	}()

	for {
		select {
		case <-writerCtx.Done():
			h.logger.Debugf("Context cancelled for client: %s", remoteAddr)
			return StreamResult{bytesWritten, writerCtx.Err(), proxy.StatusClientClosed}

		default:
			chunks, errChunk, newPos := h.coordinator.ReadChunks(lastPosition)

			// Process any available chunks first
			if len(chunks) > 0 {
				for _, chunk := range chunks {
					// Check context before each write
					if writerCtx.Err() != nil {
						// Clean up remaining chunks
						for _, c := range chunks {
							if c != nil {
								c.Reset()
							}
						}
						return StreamResult{bytesWritten, writerCtx.Err(), proxy.StatusClientClosed}
					}

					if chunk != nil && chunk.Buffer != nil && chunk.Buffer.Len() > 0 {
						// Protect against nil writer
						if writer == nil {
							h.logger.Error("Writer is nil")
							return StreamResult{bytesWritten, fmt.Errorf("writer is nil"), proxy.StatusServerError}
						}

						// Use a separate function for writing to handle panics
						n, err := h.safeWrite(writer, chunk.Buffer.Bytes())
						if err != nil {
							// Clean up remaining chunks
							for _, c := range chunks {
								if c != nil {
									c.Reset()
								}
							}
							return StreamResult{bytesWritten, err, proxy.StatusClientClosed}
						}
						bytesWritten += int64(n)

						if flusher, ok := writer.(StreamFlusher); ok {
							// Protect against panic in flush
							if err := h.safeFlush(flusher); err != nil {
								return StreamResult{bytesWritten, err, proxy.StatusClientClosed}
							}
						}
					}
					if chunk != nil {
						chunk.Reset()
					}
				}
			}

			// Handle any error chunk
			if errChunk != nil {
				if flusher, ok := writer.(StreamFlusher); ok {
					h.safeFlush(flusher)
				}
				return StreamResult{bytesWritten, errChunk.Error, errChunk.Status}
			}

			// Update position if we have a valid new position
			if newPos != nil {
				lastPosition = newPos
			}

			// Small sleep to prevent tight loop when no data
			if len(chunks) == 0 {
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

// safeWrite attempts to write to the writer and recovers from panics
func (h *StreamHandler) safeWrite(writer ResponseWriter, data []byte) (n int, err error) {
	defer func() {
		if r := recover(); r != nil {
			h.logger.Errorf("Panic in write: %v", r)
			err = fmt.Errorf("write failed: %v", r)
		}
	}()

	return writer.Write(data)
}

// safeFlush attempts to flush the writer and recovers from panics
func (h *StreamHandler) safeFlush(flusher StreamFlusher) error {
	defer func() {
		if r := recover(); r != nil {
			h.logger.Errorf("Panic in flush: %v", r)
		}
	}()

	flusher.Flush()
	return nil
}
