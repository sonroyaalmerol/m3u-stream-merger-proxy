package stream

import (
	"context"
	"fmt"
	"io"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/proxy/loadbalancer"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
)

type VariantStream struct {
	Bandwidth int
	URL       string
}

type M3U8StreamHandler struct {
	config      *StreamConfig
	logger      logger.Logger
	coordinator *StreamCoordinator
}

func NewM3U8StreamHandler(config *StreamConfig, coordinator *StreamCoordinator, logger logger.Logger) *M3U8StreamHandler {
	return &M3U8StreamHandler{
		config:      config,
		coordinator: coordinator,
		logger:      logger,
	}
}

func (h *M3U8StreamHandler) HandleHLSStream(
	ctx context.Context,
	lbResult *loadbalancer.LoadBalancerResult,
	writer ResponseWriter,
	remoteAddr string,
) StreamResult {
	if writer == nil {
		h.logger.Error("HandleHLSStream: writer is nil")
		return StreamResult{0, fmt.Errorf("writer is nil"), proxy.StatusServerError}
	}

	if h.coordinator == nil {
		h.logger.Error("HandleHLSStream: coordinator is nil")
		return StreamResult{0, fmt.Errorf("coordinator is nil"), proxy.StatusServerError}
	}

	var bytesWritten int64
	var resultErr error
	var status int

	// Create a new context that will be cancelled when either the original context
	// is cancelled or when we're cleaning up
	writerCtx, writerCancel := context.WithCancel(ctx)
	defer writerCancel()

	h.coordinator.writerCtxMu.Lock()
	isFirstClient := atomic.LoadInt32(&h.coordinator.clientCount) == 0
	if isFirstClient {
		h.coordinator.writerCtx = writerCtx
		h.coordinator.writerCancel = writerCancel
		go h.startHLSWriter(writerCtx, lbResult)
	}
	h.coordinator.writerCtxMu.Unlock()

	h.coordinator.RegisterClient()
	h.logger.Debugf("Client registered: %s, count: %d", remoteAddr, atomic.LoadInt32(&h.coordinator.clientCount))

	cleanup := func() {
		h.coordinator.UnregisterClient()
		currentCount := atomic.LoadInt32(&h.coordinator.clientCount)
		h.logger.Debugf("Client unregistered: %s, remaining: %d", remoteAddr, currentCount)

		if currentCount == 0 {
			h.coordinator.writerCtxMu.Lock()
			if h.coordinator.writerCancel != nil {
				h.logger.Debug("Stopping writer - no clients remaining")
				h.coordinator.writerCancel()
				h.coordinator.writerCancel = nil
			}
			h.coordinator.writerCtxMu.Unlock()
		}
	}
	defer cleanup()

	lastPosition := h.coordinator.buffer.Prev()

	// Create a done channel to signal when we're finished
	done := make(chan struct{})
	go func() {
		defer close(done)

		for {
			select {
			case <-ctx.Done():
				resultErr = ctx.Err()
				status = proxy.StatusClientClosed
				return
			default:
				chunks, errChunk, newPos := h.coordinator.ReadChunks(lastPosition)

				if len(chunks) > 0 {
					for _, chunk := range chunks {
						if chunk != nil && chunk.Buffer != nil && chunk.Buffer.Len() > 0 {
							n, err := writer.Write(chunk.Buffer.Bytes())
							if err != nil {
								// Clean up chunks
								for _, c := range chunks {
									if c != nil {
										c.Reset()
									}
								}
								resultErr = err
								status = proxy.StatusClientClosed
								return
							}
							bytesWritten += int64(n)
							if flusher, ok := writer.(http.Flusher); ok {
								flusher.Flush()
							}
						}
						if chunk != nil {
							chunk.Reset()
						}
					}
				}

				if errChunk != nil {
					resultErr = errChunk.Error
					status = errChunk.Status
					return
				}

				if newPos != nil {
					lastPosition = newPos
				}

				// Add a small sleep to prevent tight looping
				time.Sleep(10 * time.Millisecond)
			}
		}
	}()

	// Wait for either the context to be cancelled or the streaming to complete
	select {
	case <-ctx.Done():
		h.logger.Debugf("Client context cancelled: %s", remoteAddr)
		return StreamResult{bytesWritten, ctx.Err(), proxy.StatusClientClosed}
	case <-done:
		return StreamResult{bytesWritten, resultErr, status}
	}
}

func (h *M3U8StreamHandler) startHLSWriter(
	ctx context.Context,
	lbResult *loadbalancer.LoadBalancerResult,
) {
	defer func() {
		if r := recover(); r != nil {
			h.logger.Errorf("Panic in startHLSWriter: %v", r)
			h.coordinator.writeError(fmt.Errorf("internal server error"),
				proxy.StatusServerError)
		}
	}()

	client := &http.Client{}
	isEndlist := false
	mediaURL := lbResult.Response.Request.URL.String()

	// Create an LRU cache with a capacity of 1000 for processed segments.
	// This ensures that the cache never grows indefinitely.
	processedSegmentsCache, err := lru.New[string, struct{}](200)
	if err != nil {
		h.logger.Errorf("failed to create LRU cache: %v", err)
		h.coordinator.writeError(err, proxy.StatusServerError)
		return
	}

	// Create a ticker for polling
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for !isEndlist {
		select {
		case <-ctx.Done():
			h.logger.Debug("Context cancelled, stopping HLS writer")
			h.coordinator.writeError(ctx.Err(), proxy.StatusClientClosed)
			return
		case <-ticker.C:
			reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			segments, endlist, err := h.fetchMediaPlaylist(reqCtx, mediaURL, client)
			cancel()
			if err != nil {
				if ctx.Err() != nil {
					h.logger.Debug("Context cancelled during playlist fetch")
					h.coordinator.writeError(ctx.Err(), proxy.StatusClientClosed)
					return
				}
				if h.coordinator.shouldRetry(h.coordinator.getTimeoutDuration()) {
					continue
				}
				h.coordinator.writeError(err, proxy.StatusServerError)
				return
			}
			isEndlist = endlist
			newSegments := make([]string, 0)
			for _, segment := range segments {
				// Check if the segment is in the LRU cache.
				if !processedSegmentsCache.Contains(segment) {
					newSegments = append(newSegments, segment)
					processedSegmentsCache.Add(segment, struct{}{})
				}
			}
			if len(newSegments) > 0 {
				if err := h.streamSegments(ctx, newSegments); err != nil {
					if ctx.Err() != nil {
						h.logger.Debug("Context cancelled during segment streaming")
						h.coordinator.writeError(ctx.Err(), proxy.StatusClientClosed)
						return
					}
					if h.coordinator.shouldRetry(h.coordinator.getTimeoutDuration()) {
						continue
					}
					h.coordinator.writeError(err, proxy.StatusServerError)
					return
				}
			}
			if isEndlist {
				h.logger.Debug("Playlist ended, stopping HLS writer")
				h.coordinator.writeError(io.EOF, proxy.StatusEOF)
				return
			}
		}
	}
}

func (h *M3U8StreamHandler) streamSegments(ctx context.Context, segments []string) error {
	client := &http.Client{}

	for _, segmentURL := range segments {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			h.logger.Debugf("Fetching segment: %s", segmentURL)

			req, err := http.NewRequestWithContext(ctx, "GET", segmentURL, nil)
			if err != nil {
				h.logger.Errorf("Error creating segment request: %v", err)
				continue
			}

			segResp, err := client.Do(req)
			if err != nil {
				h.logger.Errorf("Error fetching segment: %v", err)
				continue
			}

			chunk := newChunkData()
			_, err = io.Copy(chunk.Buffer, segResp.Body)
			segResp.Body.Close()

			if err != nil {
				h.logger.Errorf("Error copying segment data: %v", err)
				chunk.Reset()
				continue
			}

			h.logger.Debugf("Writing segment of size: %d bytes", chunk.Buffer.Len())
			chunk.Timestamp = time.Now()
			if !h.coordinator.Write(chunk) {
				chunk.Reset()
				return fmt.Errorf("failed to write chunk")
			}
		}
	}
	return nil
}

func (h *M3U8StreamHandler) fetchMediaPlaylist(ctx context.Context, mediaURL string, client *http.Client) ([]string, bool, error) {
	h.logger.Debugf("Fetching media playlist from: %s", mediaURL)

	req, err := http.NewRequestWithContext(ctx, "GET", mediaURL, nil)
	if err != nil {
		return nil, false, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("failed to fetch playlist: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, fmt.Errorf("failed to read playlist body: %w", err)
	}

	h.logger.Debugf("Playlist content:\n%s", string(body))

	lines := strings.Split(string(body), "\n")
	base, _ := url.Parse(mediaURL)

	var segments []string
	isEndlist := false

	for _, line := range lines {
		line = strings.TrimSpace(line)

		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}

		if line == "#EXT-X-ENDLIST" {
			isEndlist = true
			continue
		}

		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		segURL, err := url.Parse(line)
		if err != nil {
			h.logger.Errorf("Error parsing segment URL: %v", err)
			continue
		}

		if !segURL.IsAbs() {
			segURL = base.ResolveReference(segURL)
		}

		h.logger.Debugf("Found segment: %s", segURL.String())
		segments = append(segments, segURL.String())
	}

	h.logger.Debugf("Found %d segments, endlist: %v", len(segments), isEndlist)
	return segments, isEndlist, nil
}
