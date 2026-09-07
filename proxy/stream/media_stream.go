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

func (h *StreamHandler) HandleDirectStream(
	ctx context.Context,
	lbResult *loadbalancer.LoadBalancerResult,
	streamClient *client.StreamClient,
) StreamResult {
	remoteAddr := ""
	if streamClient.Request != nil {
		remoteAddr = streamClient.Request.RemoteAddr
	}

	defer lbResult.Response.Body.Close()
	buf := make([]byte, 256*1024)

	type readResult struct {
		n   int
		err error
	}
	readChan := make(chan readResult, 1)
	doneCh := make(chan struct{}, 1)
	go func() {
		for {
			n, err := lbResult.Response.Body.Read(buf)
			select {
			case readChan <- readResult{n, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
			select {
			case <-doneCh:
			case <-ctx.Done():
				return
			}
		}
	}()

	streamClient.ResponseHeaders = lbResult.Response.Header
	_ = streamClient.WriteHeader(lbResult.Response.StatusCode)

	var bytesWritten int64
	for {
		select {
		case <-ctx.Done():
			return StreamResult{bytesWritten, fmt.Errorf("Context canceled for stream: %s", remoteAddr), proxy.StatusClientClosed}
		case r := <-readChan:
			if r.n > 0 {
				bytesWritten += int64(r.n)
				if _, werr := streamClient.Write(buf[:r.n]); werr != nil {
					return StreamResult{bytesWritten, fmt.Errorf("Server error for stream: %s", remoteAddr), proxy.StatusClientClosed}
				}
				streamClient.Flush()
			}
			switch r.err {
			case nil:
				doneCh <- struct{}{}
			case io.EOF:
				return StreamResult{bytesWritten, fmt.Errorf("EOF reached for stream: %s", remoteAddr), proxy.StatusEOF}
			default:
				return StreamResult{bytesWritten, fmt.Errorf("Server error for stream: %s", remoteAddr), proxy.StatusServerError}
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
		writerCtx := h.coordinator.WriterCtx
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
				h.coordinator.StartHLSWriter(writerCtx, lbResult, streamClient)
			} else {
				h.coordinator.StartMediaWriter(writerCtx, lbResult)
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
			}
			h.coordinator.WriterCtx = nil
			h.coordinator.WriterCtxMu.Unlock()

			h.coordinator.LastError.Store((*buffer.ChunkData)(nil))
			h.coordinator.ClearBuffer()
		}
	}
	defer cleanup()

	var (
		bytesWritten int64
		lastPosition = h.coordinator.InitialPosition()
		lastSeq      int64
	)

	for {
		select {
		case <-ctx.Done():
			h.logger.Debugf("Reader context cancelled for client: %s", remoteAddr)
			return StreamResult{bytesWritten, ctx.Err(), proxy.StatusClientClosed}

		default:
			chunks, errChunk, newPos, newSeq := h.coordinator.ReadChunks(ctx, lastPosition, lastSeq)
			lastSeq = newSeq

			// Process any available chunks first
			if len(chunks) > 0 {
				for _, chunk := range chunks {
					if ctx.Err() != nil {
						return StreamResult{bytesWritten, ctx.Err(), proxy.StatusClientClosed}
					}

					if chunk != nil && len(chunk.Data) > 0 {
						if !streamClient.IsWritable() {
							h.logger.Error("Writer is nil")
							return StreamResult{bytesWritten, fmt.Errorf("writer is nil"), proxy.StatusServerError}
						}

						h.coordinator.WaitHeaders(ctx)
						respHeaders := h.coordinator.WriterRespHeader.Load()
						if respHeaders == nil {
							respHeaders = &http.Header{}
						}

						contentType := respHeaders.Get("Content-Type")
						if !safeConcatTypes[strings.ToLower(contentType)] && utils.IsAnM3U8Media(lbResult.Response) {
							return StreamResult{bytesWritten, fmt.Errorf("%s cannot be safely concatenated and is not supported by this proxy.", contentType), proxy.StatusIncompatible}
						}
						liveHeaders := respHeaders.Clone()
						liveHeaders.Del("Content-Length")
						liveHeaders.Del("Content-Range")
						streamClient.ResponseHeaders = liveHeaders

						n, err := h.safeWrite(streamClient, chunk.Data)
						if err != nil {
							return StreamResult{bytesWritten, err, proxy.StatusClientClosed}
						}
						bytesWritten += int64(n)

						if err := h.safeFlush(streamClient); err != nil {
							return StreamResult{bytesWritten, err, proxy.StatusClientClosed}
						}
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
