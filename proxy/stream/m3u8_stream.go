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

	// Initialize the writer context before registering client
	h.coordinator.writerCtxMu.Lock()
	if h.coordinator.writerCtx == nil {
		h.coordinator.writerCtx, h.coordinator.writerCancel = context.WithCancel(context.Background())
		go h.startHLSWriter(h.coordinator.writerCtx, lbResult)
	}
	h.coordinator.writerCtxMu.Unlock()

	// Let MediaStreamHandler handle the actual streaming
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

	mediaURL := lbResult.Response.Request.URL.String()

	// Parse the initial playlist
	body, err := io.ReadAll(lbResult.Response.Body)
	if err != nil {
		h.coordinator.writeError(err, proxy.StatusServerError)
		return
	}
	defer lbResult.Response.Body.Close()

	metadata, err := h.parsePlaylist(mediaURL, string(body))
	if err != nil {
		h.coordinator.writeError(err, proxy.StatusServerError)
		return
	}

	if metadata.IsMaster {
		h.coordinator.writeError(fmt.Errorf("master playlist not supported"), proxy.StatusServerError)
		return
	}

	lastSequence := metadata.MediaSequence
	noChangeTimeout := time.After(time.Duration(h.config.TimeoutSeconds) * time.Second)

	// Process initial segments
	for _, segment := range metadata.Segments {
		select {
		case <-ctx.Done():
			h.coordinator.writeError(ctx.Err(), proxy.StatusClientClosed)
			return
		default:
			if err := h.streamSegment(ctx, segment); err != nil {
				if ctx.Err() != nil {
					h.coordinator.writeError(ctx.Err(), proxy.StatusClientClosed)
					return
				}
				h.logger.Errorf("Error streaming segment: %v", err)
				continue
			}
		}
	}

	if metadata.IsEndlist {
		h.coordinator.writeError(io.EOF, proxy.StatusEOF)
		return
	}

	// For live streams, poll for updates
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			h.coordinator.writeError(ctx.Err(), proxy.StatusClientClosed)
			return

		case <-noChangeTimeout:
			h.logger.Debug("No new segments detected within timeout period")
			h.coordinator.writeError(fmt.Errorf("stream timeout: no new segments"), proxy.StatusEOF)
			return

		case <-ticker.C:
			// Fetch current playlist
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

			if metadata.MediaSequence > lastSequence {
				// New segments found, reset timeout
				noChangeTimeout = time.After(time.Duration(h.config.TimeoutSeconds) * time.Second)
				lastSequence = metadata.MediaSequence

				for _, segment := range metadata.Segments {
					if err := h.streamSegment(ctx, segment); err != nil {
						if ctx.Err() != nil {
							h.coordinator.writeError(ctx.Err(), proxy.StatusClientClosed)
							return
						}
						h.logger.Errorf("Error streaming segment: %v", err)
					}
				}
			}
		}
	}
}

func (h *M3U8StreamHandler) streamSegment(ctx context.Context, segmentURL string) error {
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

	chunk := newChunkData()
	defer func() {
		if chunk != nil {
			chunk.Reset()
		}
	}()

	if _, err := io.Copy(chunk.Buffer, resp.Body); err != nil {
		return fmt.Errorf("failed to copy segment data: %w", err)
	}

	chunk.Timestamp = time.Now()
	if !h.coordinator.Write(chunk) {
		return fmt.Errorf("failed to write chunk")
	}

	// Chunk was successfully written, don't reset it
	chunk = nil
	return nil
}

func (h *M3U8StreamHandler) parsePlaylist(mediaURL string, content string) (*PlaylistMetadata, error) {
	metadata := &PlaylistMetadata{
		Segments: make([]string, 0, 32),
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
