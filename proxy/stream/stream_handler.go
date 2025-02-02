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

	"github.com/valyala/bytebufferpool"
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
	h.coordinator.RegisterClient()
	defer h.coordinator.UnregisterClient()
	if atomic.LoadInt32(&h.coordinator.clientCount) == 1 {
		h.logger.Debugf("Starting writer goroutine for m3uIndex: %s", m3uIndex)
		go h.coordinator.StartWriter(ctx, m3uIndex, resp)
	}
	var bytesWritten int64
	var lastPosition *ring.Ring
	lastPosition = h.coordinator.buffer
	batchBuffer := bytebufferpool.Get()
	defer bytebufferpool.Put(batchBuffer)

	for {
		select {
		case <-ctx.Done():
			return StreamResult{bytesWritten, ctx.Err(), proxy.StatusClientClosed}
		default:
			// First read chunks
			chunks, errChunk, newPos := h.coordinator.ReadChunks(lastPosition)

			// Process any available chunks
			if len(chunks) > 0 {
				batchBuffer.Reset()
				for _, chunk := range chunks {
					batchBuffer.Write(chunk.Buffer.B)
					chunk.Reset() // Return buffers to pool immediately
				}
				if written, err := writer.Write(batchBuffer.B); err != nil {
					return StreamResult{bytesWritten, err, 0}
				} else {
					bytesWritten += int64(written)
				}
				if flusher, ok := writer.(StreamFlusher); ok {
					flusher.Flush()
				}
			}
			lastPosition = newPos

			// After processing chunks, check for error
			if errChunk != nil {
				return StreamResult{bytesWritten, errChunk.Error, errChunk.Status}
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
