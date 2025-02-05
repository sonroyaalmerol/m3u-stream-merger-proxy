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

	// Only initialize the HLS writer if needed
	h.coordinator.writerCtxMu.Lock()
	if !h.coordinator.writerActive.Load() {
		if h.coordinator.writerCtx == nil {
			h.coordinator.writerCtx, h.coordinator.writerCancel = context.WithCancel(context.Background())
		}
		go h.startHLSWriter(h.coordinator.writerCtx, lbResult)
	}
	h.coordinator.writerCtxMu.Unlock()

	// Let MediaStreamHandler handle all client registration and streaming
	mediaHandler := NewMediaStreamHandler(h.config, h.coordinator, h.logger)
	return mediaHandler.HandleMediaStream(ctx, lbResult, writer, remoteAddr)
}

type PlaylistMetadata struct {
	TargetDuration float64
	MediaSequence  int64
	Version        int
	IsEndlist      bool
	Segments       []string
	IsMaster       bool
}

func (h *M3U8StreamHandler) startHLSWriter(ctx context.Context, lbResult *loadbalancer.LoadBalancerResult) {
	defer func() {
		if r := recover(); r != nil {
			h.logger.Errorf("Panic in startHLSWriter: %v", r)
			h.coordinator.writeError(fmt.Errorf("internal server error"), proxy.StatusServerError)
		}
	}()

	mediaURL := lbResult.Response.Request.URL.String()
	lastSequence := int64(-1)
	seenSegments := make(map[string]bool)
	retryAttempts := 0
	maxRetries := 5

	// Start with a short interval and adjust based on target duration
	interval := time.Second
	ticker := time.NewTicker(interval)
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

				retryAttempts++
				h.logger.Warnf("Failed to fetch playlist metadata (attempt %d/%d): %v",
					retryAttempts, maxRetries, err)

				if retryAttempts >= maxRetries {
					h.logger.Error("Max retry attempts reached, stopping HLS writer")
					h.coordinator.writeError(err, proxy.StatusServerError)
					return
				}

				// Exponential backoff for retries
				time.Sleep(time.Duration(retryAttempts*retryAttempts) * 100 * time.Millisecond)
				continue
			}
			retryAttempts = 0 // Reset retry counter on successful fetch

			// Handle master playlist
			if metadata.IsMaster {
				h.logger.Error("Master playlist not supported in this context")
				h.coordinator.writeError(fmt.Errorf("master playlist not supported"), proxy.StatusServerError)
				return
			}

			// Update polling interval based on target duration
			if metadata.TargetDuration > 0 {
				newInterval := time.Duration(metadata.TargetDuration * float64(time.Second) / 2)
				if newInterval != interval {
					interval = newInterval
					ticker.Reset(interval)
				}
			}

			// Process new segments
			if len(metadata.Segments) > 0 {
				// For live streams, we need to handle the case where MediaSequence resets
				if metadata.MediaSequence < lastSequence {
					h.logger.Log("Media sequence reset detected, clearing segment history")
					lastSequence = metadata.MediaSequence - 1
					seenSegments = make(map[string]bool)
				}

				for i, segment := range metadata.Segments {
					segmentSeq := metadata.MediaSequence + int64(i)

					// Skip already processed segments
					if segmentSeq <= lastSequence || seenSegments[segment] {
						continue
					}

					if err := h.streamSegment(ctx, segment); err != nil {
						if ctx.Err() != nil {
							return
						}
						h.logger.Errorf("Error streaming segment: %v", err)
						continue
					}

					seenSegments[segment] = true
					lastSequence = segmentSeq

					// Cleanup old segments from map to prevent memory growth
					if len(seenSegments) > 1000 {
						newSeenSegments := make(map[string]bool)
						for j := max(0, i-100); j <= i; j++ {
							if j < len(metadata.Segments) {
								newSeenSegments[metadata.Segments[j]] = true
							}
						}
						seenSegments = newSeenSegments
					}
				}
			}

			// Only exit if we've seen the endlist marker
			if metadata.IsEndlist {
				h.logger.Debug("Playlist ended (EXT-X-ENDLIST found)")
				h.coordinator.writeError(io.EOF, proxy.StatusEOF)
				return
			}

			// For live streams with no new segments, keep polling
			if len(metadata.Segments) == 0 {
				h.logger.Debug("No new segments found, continuing to poll")
				continue
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

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("segment request failed with status: %d", resp.StatusCode)
	}

	chunk := newChunkData()
	if _, err := io.Copy(chunk.Buffer, resp.Body); err != nil {
		chunk.Reset()
		return fmt.Errorf("error copying segment data: %w", err)
	}

	chunk.Timestamp = time.Now()
	if !h.coordinator.Write(chunk) {
		chunk.Reset()
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

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("playlist request failed with status: %d", resp.StatusCode)
	}

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
		case strings.HasPrefix(line, "#EXTM3U"):
			continue

		case strings.HasPrefix(line, "#EXT-X-STREAM-INF"):
			metadata.IsMaster = true
			return metadata, nil

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

	if metadata.TargetDuration == 0 {
		h.logger.Warn("No EXT-X-TARGETDURATION found in playlist, using default")
		metadata.TargetDuration = 2
	}

	return metadata, nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
