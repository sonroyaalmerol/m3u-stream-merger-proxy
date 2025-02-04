package stream

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/proxy/loadbalancer"
	"m3u-stream-merger/utils"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

type VariantStream struct {
	Bandwidth int
	URL       string
}

type M3U8StreamHandler struct {
	config      *StreamConfig
	logger      logger.Logger
	coordinator *StreamCoordinator
	client      *http.Client
}

func NewM3U8StreamHandler(config *StreamConfig, coordinator *StreamCoordinator, logger logger.Logger) *M3U8StreamHandler {
	return &M3U8StreamHandler{
		config:      config,
		coordinator: coordinator,
		logger:      logger,
		client: &http.Client{
			Timeout: time.Duration(config.TimeoutSeconds) * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:       100,
				IdleConnTimeout:    90 * time.Second,
				DisableCompression: false, // Enable compression
				MaxConnsPerHost:    100,
				DisableKeepAlives:  false,
				ForceAttemptHTTP2:  true, // Enable HTTP/2 support
			},
			CheckRedirect: utils.HTTPClient.CheckRedirect,
		},
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

	if err := h.coordinator.RegisterClient(); err != nil {
		return StreamResult{0, err, proxy.StatusServerError}
	}
	h.logger.Debugf("Client registered: %s, count: %d", remoteAddr, atomic.LoadInt32(&h.coordinator.clientCount))

	h.coordinator.writerCtxMu.Lock()
	isFirstClient := atomic.LoadInt32(&h.coordinator.clientCount) == 1
	if isFirstClient {
		h.coordinator.writerCtx, h.coordinator.writerCancel = context.WithCancel(context.Background())
		go h.startHLSWriter(h.coordinator.writerCtx, lbResult)
	}
	h.coordinator.writerCtxMu.Unlock()

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

type PlaylistMetadata struct {
	TargetDuration float64
	MediaSequence  int64
	Version        int
	IsEndlist      bool
	Segments       []string
}

func (h *M3U8StreamHandler) startHLSWriter(ctx context.Context, lbResult *loadbalancer.LoadBalancerResult) {
	defer func() {
		if r := recover(); r != nil {
			h.logger.Errorf("Panic in startHLSWriter: %v", r)
			h.coordinator.writeError(fmt.Errorf("internal server error"), proxy.StatusServerError)
		}
	}()

	mediaURL := lbResult.Response.Request.URL.String()
	lastSequence := int64(-1)             // Track the last sequence we processed
	ticker := time.NewTicker(time.Second) // Start with default interval
	defer ticker.Stop()

	for atomic.LoadInt32(&h.coordinator.state) == stateActive {
		select {
		case <-ctx.Done():
			h.logger.Debug("Context cancelled, stopping HLS writer")
			h.coordinator.writeError(ctx.Err(), proxy.StatusClientClosed)
			return
		case <-ticker.C:
			metadata, err := h.fetchPlaylistMetadata(ctx, mediaURL)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				continue
			}

			// Update polling interval based on target duration
			if metadata.TargetDuration > 0 {
				newInterval := time.Duration(metadata.TargetDuration * float64(time.Second) / 2)
				ticker.Reset(newInterval)
			}

			// Only process new segments if the sequence is newer
			if metadata.MediaSequence > lastSequence {
				// Process segments in order
				for i, segment := range metadata.Segments {
					segmentSeq := metadata.MediaSequence + int64(i)
					if segmentSeq <= lastSequence {
						continue // Skip segments we've already processed
					}

					if err := h.streamSegment(ctx, segment); err != nil {
						if ctx.Err() != nil {
							return
						}
						h.logger.Errorf("Error streaming segment: %v", err)
						continue
					}
					lastSequence = segmentSeq
				}
			}

			if metadata.IsEndlist {
				h.logger.Debug("Playlist ended, stopping HLS writer")
				h.coordinator.writeError(io.EOF, proxy.StatusEOF)
				return
			}
		}
	}
}

func (h *M3U8StreamHandler) streamSegment(ctx context.Context, segmentURL string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", segmentURL, nil)
	if err != nil {
		return fmt.Errorf("error creating segment request: %w", err)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return fmt.Errorf("error fetching segment: %w", err)
	}
	defer resp.Body.Close()

	chunk := newChunkData()
	defer chunk.Reset()

	if _, err := io.Copy(chunk.Buffer, resp.Body); err != nil {
		return fmt.Errorf("error copying segment data: %w", err)
	}

	chunk.Timestamp = time.Now()
	if !h.coordinator.Write(chunk) {
		return fmt.Errorf("failed to write chunk")
	}

	return nil
}

func (h *M3U8StreamHandler) fetchPlaylistMetadata(ctx context.Context, mediaURL string) (*PlaylistMetadata, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", mediaURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch playlist: %w", err)
	}
	defer resp.Body.Close()

	limitedReader := io.LimitReader(resp.Body, 1024*1024) // 1MB limit for playlist
	scanner := bufio.NewScanner(limitedReader)

	metadata := &PlaylistMetadata{
		Segments: make([]string, 0, 32),
	}

	base, err := url.Parse(mediaURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse base URL: %w", err)
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		switch {
		case strings.HasPrefix(line, "#EXT-X-VERSION:"):
			if _, err := fmt.Sscanf(line, "#EXT-X-VERSION:%d", &metadata.Version); err != nil {
				h.logger.Warnf("Failed to parse version: %v", err)
			}

		case strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"):
			if _, err := fmt.Sscanf(line, "#EXT-X-TARGETDURATION:%f", &metadata.TargetDuration); err != nil {
				h.logger.Warnf("Failed to parse target duration: %v", err)
			}

		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			if _, err := fmt.Sscanf(line, "#EXT-X-MEDIA-SEQUENCE:%d", &metadata.MediaSequence); err != nil {
				h.logger.Warnf("Failed to parse media sequence: %v", err)
			}

		case line == "#EXT-X-ENDLIST":
			metadata.IsEndlist = true

		case line != "" && !strings.HasPrefix(line, "#"):
			segURL, err := url.Parse(line)
			if err != nil {
				h.logger.Warnf("Skipping invalid segment URL %q: %v", line, err)
				continue
			}

			if !segURL.IsAbs() {
				segURL = base.ResolveReference(segURL)
			}
			metadata.Segments = append(metadata.Segments, segURL.String())
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error scanning playlist: %w", err)
	}

	// Validate required fields
	if metadata.TargetDuration == 0 {
		h.logger.Warn("No EXT-X-TARGETDURATION found in playlist, using default")
		metadata.TargetDuration = 2 // Default to 2 seconds if not specified
	}

	return metadata, nil
}
