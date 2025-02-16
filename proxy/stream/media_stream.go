package stream

import (
	"context"
	"fmt"
	"io"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/proxy/client"
	"m3u-stream-merger/proxy/loadbalancer"
	"m3u-stream-merger/proxy/stream/buffer"
	"m3u-stream-merger/proxy/stream/config"
	"m3u-stream-merger/utils"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

var safeConcatTypes = map[string]bool{
	"video/mp2t": true,
	"video/mpeg": true,
	"audio/aac":  true, // AAC in ADTS format can be concatenated
	"audio/mpeg": true, // MP3 can be concatenated
}

type StreamHandler struct {
	config      *config.StreamConfig
	logger      logger.Logger
	coordinator *buffer.StreamCoordinator
}

func NewStreamHandler(config *config.StreamConfig, coordinator *buffer.StreamCoordinator, logger logger.Logger) *StreamHandler {
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

func (h *StreamHandler) HandleVOD(
	ctx context.Context,
	lbResult *loadbalancer.LoadBalancerResult,
	streamClient *client.StreamClient,
) StreamResult {
	remoteAddr := ""
	if streamClient.Request != nil {
		remoteAddr = streamClient.Request.RemoteAddr
	}
	h.logger.Logf("VOD request detected from: %s", remoteAddr)
	h.logger.Warn("VODs do not support shared buffer.")

	buffer := make([]byte, h.config.ChunkSize*h.config.SharedBufferSize)
	readChan := make(chan struct {
		n   int
		err error
	}, 1)

	defer lbResult.Response.Body.Close()

	streamClient.ResponseHeaders = lbResult.Response.Header
	_ = streamClient.WriteHeader(lbResult.Response.StatusCode)

	for {
		go func() {
			n, err := lbResult.Response.Body.Read(buffer)
			readChan <- struct {
				n   int
				err error
			}{n, err}
		}()

		select {
		case <-ctx.Done():
			return StreamResult{0, fmt.Errorf("Context canceled for VOD: %s", remoteAddr), proxy.StatusClientClosed}
		case result := <-readChan:
			switch {
			case result.err == io.EOF:
				return StreamResult{0, fmt.Errorf("EOF reached for VOD: %s", remoteAddr), proxy.StatusEOF}
			case result.err != nil:
				return StreamResult{0, fmt.Errorf("Server error for VOD: %s", remoteAddr), proxy.StatusServerError}
			case result.err == nil:
				if _, err := streamClient.Write(buffer[:result.n]); err != nil {
					return StreamResult{0, fmt.Errorf("Server error for VOD: %s", remoteAddr), proxy.StatusClientClosed}
				}

				streamClient.Flush()
			}
		}
	}
}

func (h *StreamHandler) HandleStream(
	ctx context.Context,
	lbResult *loadbalancer.LoadBalancerResult,
	streamClient *client.StreamClient,
) StreamResult {
	remoteAddr := ""
	if streamClient.Request != nil {
		remoteAddr = streamClient.Request.RemoteAddr
	}
	if h.coordinator == nil {
		h.logger.Error("handleBufferedStream: coordinator is nil")
		return StreamResult{0, fmt.Errorf("coordinator is nil"), proxy.StatusServerError}
	}

	// Lock the initialization (writer-start) section.
	h.coordinator.InitializationMu.Lock()
	// Check if we have already started the writer.
	if !h.coordinator.WriterActive.Load() {
		// Mark the writer as started.
		h.coordinator.WriterActive.Store(true)

		h.coordinator.WriterCtxMu.Lock()
		if h.coordinator.WriterCtx == nil {
			h.coordinator.WriterCtx, h.coordinator.WriterCancel = context.WithCancel(context.Background())
		}
		h.coordinator.WriterCtxMu.Unlock()

		h.coordinator.LastError.Store((*buffer.ChunkData)(nil))
		h.coordinator.ClearBuffer()

		// Start the writer in its own goroutine.
		go func() {
			// When the writer stops, reset the flag.
			defer func() {
				h.coordinator.InitializationMu.Lock()
				h.coordinator.WriterActive.Store(false)
				h.coordinator.InitializationMu.Unlock()
			}()
			if utils.IsAnM3U8Media(lbResult.Response) {
				h.coordinator.StartHLSWriter(h.coordinator.WriterCtx, lbResult)
			} else {
				h.coordinator.StartMediaWriter(h.coordinator.WriterCtx, lbResult)
			}
		}()
	}

	if err := h.coordinator.RegisterClient(); err != nil {
		h.coordinator.InitializationMu.Unlock()
		return StreamResult{0, err, proxy.StatusServerError}
	}
	h.coordinator.InitializationMu.Unlock()

	h.logger.Debugf("Client registered: %s, count: %d", remoteAddr, atomic.LoadInt32(&h.coordinator.ClientCount))

	cleanup := func() {
		h.coordinator.UnregisterClient()
		currentCount := atomic.LoadInt32(&h.coordinator.ClientCount)
		h.logger.Debugf("Client unregistered: %s, remaining: %d", remoteAddr, currentCount)

		if currentCount == 0 {
			h.coordinator.WriterCtxMu.Lock()
			if h.coordinator.WriterCancel != nil {
				h.logger.Debug("Stopping writer - no clients remaining")
				h.coordinator.WriterCancel()
				h.coordinator.WriterCancel = nil
				h.coordinator.WriterCtx = nil
			}
			h.coordinator.WriterCtx = nil
			h.coordinator.WriterCtxMu.Unlock()

			h.coordinator.LastError.Store((*buffer.ChunkData)(nil))
			h.coordinator.ClearBuffer()

			h.coordinator.Mu.Lock()
			h.coordinator.WriterChan = make(chan struct{}, 1)
			h.coordinator.Mu.Unlock()
		}
	}
	defer cleanup()

	var bytesWritten int64
	lastPosition := h.coordinator.Buffer.Prev() // Start from previous to get first new chunk

	// Create a channel to signal client helper goroutine to stop
	done := make(chan struct{})
	defer close(done)

	// Create a context for this client
	readerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Handle context cancellation in a separate goroutine
	go func() {
		select {
		case <-ctx.Done():
			h.logger.Debugf("Client context cancelled: %s", remoteAddr)
			cancel()
		case <-done:
			return
		}
	}()

	for {
		select {
		case <-readerCtx.Done():
			h.logger.Debugf("Reader context cancelled for client: %s", remoteAddr)
			return StreamResult{bytesWritten, readerCtx.Err(), proxy.StatusClientClosed}

		default:
			chunks, errChunk, newPos := h.coordinator.ReadChunks(lastPosition)

			// Process any available chunks first
			if len(chunks) > 0 {
				for _, chunk := range chunks {
					// Check context before each write
					if readerCtx.Err() != nil {
						// Clean up remaining chunks
						for _, c := range chunks {
							if c != nil {
								c.Reset()
							}
						}
						return StreamResult{bytesWritten, readerCtx.Err(), proxy.StatusClientClosed}
					}

					if chunk != nil && chunk.Buffer != nil && chunk.Buffer.Len() > 0 {
						// Protect against nil writer
						if !streamClient.IsWritable() {
							h.logger.Error("Writer is nil")
							return StreamResult{bytesWritten, fmt.Errorf("writer is nil"), proxy.StatusServerError}
						}

						// ensure headers are set for reader
						h.coordinator.WaitHeaders(readerCtx)
						respHeaders := h.coordinator.WriterRespHeader.Load()
						if respHeaders == nil {
							respHeaders = &http.Header{}
						}

						contentType := respHeaders.Get("Content-Type")
						if !safeConcatTypes[strings.ToLower(contentType)] && utils.IsAnM3U8Media(lbResult.Response) {
							return StreamResult{bytesWritten, fmt.Errorf("%s cannot be safely concatenated and is not supported by this proxy.", contentType), proxy.StatusIncompatible}
						}
						streamClient.ResponseHeaders = *respHeaders

						// Use a separate function for writing to handle panics
						n, err := h.safeWrite(streamClient, chunk.Buffer.Bytes())
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

						// Protect against panic in flush
						if err := h.safeFlush(streamClient); err != nil {
							return StreamResult{bytesWritten, err, proxy.StatusClientClosed}
						}
					}
					if chunk != nil {
						chunk.Reset()
					}
				}
			}

			// Handle any error chunk
			if errChunk != nil {
				h.safeFlush(streamClient)
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

// safeWrite attempts to write to the writer and recovers from panics
func (h *StreamHandler) safeWrite(streamClient *client.StreamClient, data []byte) (n int, err error) {
	defer func() {
		if r := recover(); r != nil {
			h.logger.Errorf("Panic in write: %v", r)
			err = fmt.Errorf("write failed: %v", r)
		}
	}()

	return streamClient.Write(data)
}

// safeFlush attempts to flush the writer and recovers from panics
func (h *StreamHandler) safeFlush(streamClient *client.StreamClient) error {
	defer func() {
		if r := recover(); r != nil {
			h.logger.Errorf("Panic in flush: %v", r)
		}
	}()

	streamClient.Flush()
	return nil
}
