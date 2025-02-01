package stream

import (
	"context"
	"io"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/utils"
	"net/http"
	"time"
)

type StreamHandler struct {
	config *StreamConfig
	logger logger.Logger
}

func NewStreamHandler(config *StreamConfig, logger logger.Logger) *StreamHandler {
	return &StreamHandler{
		config: config,
		logger: logger,
	}
}

type StreamResult struct {
	BytesWritten int64
	Error        error
	Status       int
}

func (h *StreamHandler) HandleStream(
	ctx context.Context,
	resp *http.Response,
	writer proxy.ResponseWriter,
	remoteAddr string,
) StreamResult {
	buffer := h.createBuffer()
	defer func() {
		buffer = nil
		resp.Body.Close() // Ensure we always close the response body
	}()

	timeoutDuration := h.getTimeoutDuration()
	backoff := proxy.NewBackoffStrategy(h.config.InitialBackoff, time.Duration(h.config.TimeoutSeconds-1)*time.Second)

	timeStarted := time.Now()
	lastErr := timeStarted
	var bytesWritten int64

	for {
		select {
		case <-ctx.Done():
			// Client disconnected, clean up and return
			h.logger.Debugf("Client disconnected from stream: %s", remoteAddr)
			return StreamResult{bytesWritten, ctx.Err(), proxy.StatusClientClosed}

		default:
			result := h.readChunk(ctx, resp, buffer)

			if h.shouldTimeout(timeStarted, timeoutDuration) {
				h.logger.Errorf("Timeout reached while trying to stream: %s", remoteAddr)
				return StreamResult{bytesWritten, nil, proxy.StatusServerError}
			}

			switch {
			case result.Error == context.Canceled:
				// Handle context cancellation from readChunk
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
					if flusher, ok := writer.(proxy.StreamFlusher); ok {
						flusher.Flush()
					}
				}
				return h.handleEOF(resp, remoteAddr, bytesWritten)

			case result.Error != nil:
				if h.shouldRetry(timeoutDuration) {
					h.logger.Errorf("Error reading stream: %s", result.Error.Error())
					backoff.Sleep(ctx)
					lastErr = time.Now()
					continue
				}
				return StreamResult{bytesWritten, result.Error, proxy.StatusServerError}

			default:
				n := result.N
				totalWritten := 0
				for totalWritten < n {
					written, err := writer.Write(buffer[totalWritten:n])
					if err != nil {
						h.logger.Errorf("Error writing to response: %s", err.Error())
						return StreamResult{bytesWritten, err, 0}
					}
					totalWritten += written
				}
				bytesWritten += int64(n)
				if flusher, ok := writer.(proxy.StreamFlusher); ok {
					flusher.Flush()
				}

				if h.shouldResetTimer(lastErr, timeStarted) {
					timeStarted = time.Now()
					backoff.Reset()
				}
			}
		}
	}
}

func (h *StreamHandler) createBuffer() []byte {
	if h.config.BufferSizeMB > 0 {
		return make([]byte, h.config.BufferSizeMB*1024*1024)
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

func (h *StreamHandler) shouldTimeout(
	timeStarted time.Time,
	timeout time.Duration,
) bool {
	return h.config.TimeoutSeconds > 0 && time.Since(timeStarted) >= timeout
}

func (h *StreamHandler) shouldRetry(timeout time.Duration) bool {
	return h.config.TimeoutSeconds == 0 || timeout > 0
}

func (h *StreamHandler) shouldResetTimer(lastErr, timeStarted time.Time) bool {
	return lastErr.Equal(timeStarted) || time.Since(lastErr) >= time.Second
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
