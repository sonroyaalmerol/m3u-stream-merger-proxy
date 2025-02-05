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
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var safeConcatTypes = map[string]bool{
	"video/mp2t": true,
	"video/mpeg": true,
	"audio/aac":  true, // AAC in ADTS format can be concatenated
	"audio/mpeg": true, // MP3 can be concatenated
}

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

type PlaylistMetadata struct {
	TargetDuration float64
	MediaSequence  int64
	Version        int
	IsEndlist      bool
	Segments       []string
	IsMaster       bool
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
	if writer == nil || h.coordinator == nil {
		h.logger.Error("HandleHLSStream: invalid parameters")
		return StreamResult{0, fmt.Errorf("invalid parameters"), proxy.StatusServerError}
	}

	// Initialize the writer context before starting writer
	h.coordinator.writerCtxMu.Lock()
	if h.coordinator.writerCtx == nil {
		h.coordinator.writerCtx, h.coordinator.writerCancel = context.WithCancel(context.Background())
		go h.startHLSWriter(h.coordinator.writerCtx, lbResult)
	}
	h.coordinator.writerCtxMu.Unlock()

	// Let MediaStreamHandler handle client registration and streaming
	mediaHandler := NewMediaStreamHandler(h.config, h.coordinator, h.logger)
	return mediaHandler.HandleMediaStream(ctx, lbResult, writer, remoteAddr)
}

func (h *M3U8StreamHandler) startHLSWriter(ctx context.Context, lbResult *loadbalancer.LoadBalancerResult) {
	defer func() {
		if r := recover(); r != nil {
			h.logger.Errorf("Panic in startHLSWriter: %v", r)
			h.coordinator.writeError(fmt.Errorf("internal server error"), proxy.StatusServerError)
		}
	}()

	if !h.coordinator.writerActive.CompareAndSwap(false, true) {
		return
	}
	defer h.coordinator.writerActive.Store(false)

	h.coordinator.firstSegmentContentType.Store("")

	mediaURL := lbResult.Response.Request.URL.String()
	lastMediaSeq := int64(-1)
	var lastErr error
	lastChangeTime := time.Now()

	// Start with a conservative polling rate
	pollInterval := time.Second
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			if lastErr == nil {
				h.coordinator.writeError(ctx.Err(), proxy.StatusClientClosed)
			}
			return

		case <-ticker.C:
			// Check timeout first
			if time.Since(lastChangeTime) > time.Duration(h.config.TimeoutSeconds)*time.Second+pollInterval {
				h.logger.Debug("No sequence changes detected within timeout period")
				h.coordinator.writeError(fmt.Errorf("stream timeout: no new segments"), proxy.StatusEOF)
				return
			}

			// Create new request for each poll
			req, err := http.NewRequestWithContext(ctx, "GET", mediaURL, nil)
			if err != nil {
				continue
			}

			resp, err := h.client.Do(req)
			if err != nil {
				continue
			}

			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				continue
			}

			metadata, err := h.parsePlaylist(mediaURL, string(body))
			if err != nil {
				continue
			}

			// Update polling rate based on target duration
			if metadata.TargetDuration > 0 {
				// HLS spec recommends polling at no less than target duration / 2
				newInterval := time.Duration(metadata.TargetDuration * float64(time.Second) / 2)

				// Add a small random jitter (±10%) to prevent thundering herd
				jitter := time.Duration(float64(newInterval) * (0.9 + 0.2*rand.Float64()))

				// Only update if significantly different (>10% change)
				if math.Abs(float64(jitter-pollInterval)) > float64(pollInterval)*0.1 {
					pollInterval = jitter
					ticker.Reset(pollInterval)
					h.logger.Debugf("Updated polling interval to %v", pollInterval)
				}
			}

			if metadata.IsMaster {
				h.coordinator.writeError(fmt.Errorf("master playlist not supported"), proxy.StatusServerError)
				return
			}

			if metadata.IsEndlist {
				// Process remaining segments before ending
				err = h.processSegments(ctx, metadata.Segments, false)
				if err != nil {
					h.logger.Errorf("Error processing segments: %v", err)
				}
				h.coordinator.writeError(io.EOF, proxy.StatusEOF)
				return
			}

			if metadata.MediaSequence > lastMediaSeq {
				lastChangeTime = time.Now()
				lastMediaSeq = metadata.MediaSequence

				// Only check content type if we haven't detected it yet
				detectContentType := h.coordinator.firstSegmentContentType.Load() == ""

				if err := h.processSegments(ctx, metadata.Segments, detectContentType); err != nil {
					if ctx.Err() != nil {
						h.coordinator.writeError(ctx.Err(), proxy.StatusClientClosed)
						return
					}
					lastErr = err
				}
			}
		}
	}
}

func (h *M3U8StreamHandler) processSegments(ctx context.Context, segments []string, detectContentType bool) error {
	for i, segment := range segments {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			if err := h.streamSegment(ctx, segment, detectContentType && i == 0); err != nil {
				h.logger.Errorf("Error streaming segment: %v", err)
				continue
			}
		}
	}
	return nil
}

func (h *M3U8StreamHandler) streamSegment(ctx context.Context, segmentURL string, detectContentType bool) error {
	req, err := http.NewRequestWithContext(ctx, "GET", segmentURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to fetch segment: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("segment request failed with status: %d", resp.StatusCode)
	}

	// Check content type if this is the first segment and we haven't detected it yet
	if detectContentType {
		contentType := resp.Header.Get("Content-Type")
		if contentType == "" {
			// Try to detect from content if header not present
			buffer := make([]byte, 512)
			n, _ := io.ReadAtLeast(resp.Body, buffer, 1)
			if n > 0 {
				contentType = http.DetectContentType(buffer[:n])
			}

			if !safeConcatTypes[strings.ToLower(contentType)] {
				return fmt.Errorf("content type %s cannot be safely concatenated", contentType)
			}

			if contentType == "" {
				contentType = "video/MP2T" // Default to MPEG-TS if we can't detect
			}
		}
		h.coordinator.firstSegmentContentType.Store(contentType)
		h.logger.Debugf("Detected segment content type: %s", contentType)
	}

	chunk := newChunkData()
	if _, err := io.Copy(chunk.Buffer, resp.Body); err != nil {
		chunk.Reset()
		return fmt.Errorf("failed to copy segment data: %w", err)
	}

	chunk.Timestamp = time.Now()
	if !h.coordinator.Write(chunk) {
		chunk.Reset()
		return fmt.Errorf("failed to write chunk")
	}

	return nil
}

func (h *M3U8StreamHandler) parsePlaylist(mediaURL string, content string) (*PlaylistMetadata, error) {
	metadata := &PlaylistMetadata{
		Segments:       make([]string, 0, 32),
		TargetDuration: 2, // Default target duration as fallback
	}

	base, err := url.Parse(mediaURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse base URL: %w", err)
	}

	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		switch {
		case strings.HasPrefix(line, "#EXTM3U"):
			continue
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF"):
			metadata.IsMaster = true
			return metadata, nil
		case strings.HasPrefix(line, "#EXT-X-VERSION:"):
			_, _ = fmt.Sscanf(line, "#EXT-X-VERSION:%d", &metadata.Version)
		case strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"):
			_, _ = fmt.Sscanf(line, "#EXT-X-TARGETDURATION:%f", &metadata.TargetDuration)
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			_, _ = fmt.Sscanf(line, "#EXT-X-MEDIA-SEQUENCE:%d", &metadata.MediaSequence)
		case line == "#EXT-X-ENDLIST":
			metadata.IsEndlist = true
		case !strings.HasPrefix(line, "#") && line != "":
			segURL, err := url.Parse(line)
			if err != nil {
				h.logger.Warnf("Invalid segment URL %q: %v", line, err)
				continue
			}

			if !segURL.IsAbs() {
				segURL = base.ResolveReference(segURL)
			}
			metadata.Segments = append(metadata.Segments, segURL.String())
		}
	}

	return metadata, scanner.Err()
}
