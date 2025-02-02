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
	"strconv"
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

	h.coordinator.writerCtxMu.Lock()
	isFirstClient := atomic.LoadInt32(&h.coordinator.clientCount) == 0
	if isFirstClient {
		h.coordinator.writerCtx, h.coordinator.writerCancel = context.WithCancel(context.Background())
		go h.startHLSWriter(h.coordinator.writerCtx, lbResult)
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

	var bytesWritten int64
	lastPosition := h.coordinator.buffer.Prev()

	for {
		select {
		case <-ctx.Done():
			h.logger.Debugf("Client context cancelled: %s", remoteAddr)
			return StreamResult{bytesWritten, ctx.Err(), proxy.StatusClientClosed}
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
							return StreamResult{bytesWritten, err, proxy.StatusClientClosed}
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
				return StreamResult{bytesWritten, errChunk.Error, errChunk.Status}
			}

			if newPos != nil {
				lastPosition = newPos
			}

			time.Sleep(10 * time.Millisecond)
		}
	}
}

func (h *M3U8StreamHandler) startHLSWriter(ctx context.Context, lbResult *loadbalancer.LoadBalancerResult) {
	defer func() {
		if r := recover(); r != nil {
			h.logger.Errorf("Panic in startHLSWriter: %v", r)
			h.coordinator.writeError(fmt.Errorf("internal server error"), proxy.StatusServerError)
		}
	}()

	// Read master playlist
	body, err := io.ReadAll(lbResult.Response.Body)
	if err != nil {
		h.coordinator.writeError(fmt.Errorf("failed to read playlist: %w", err), proxy.StatusServerError)
		return
	}
	defer lbResult.Response.Body.Close()

	content := string(body)

	// Verify this is a master playlist
	if !strings.Contains(content, "#EXT-X-STREAM-INF:") {
		h.coordinator.writeError(fmt.Errorf("not a master playlist"), proxy.StatusServerError)
		return
	}

	// Get best variant URL
	bestVariant, err := h.getBestVariant(strings.Split(content, "\n"), lbResult.Response.Request.URL)
	if err != nil {
		h.coordinator.writeError(err, proxy.StatusServerError)
		return
	}

	client := &http.Client{}
	isEndlist := false

	for !isEndlist {
		select {
		case <-ctx.Done():
			h.coordinator.writeError(ctx.Err(), proxy.StatusClientClosed)
			return
		default:
			segments, endlist, err := h.fetchMediaPlaylist(bestVariant.URL, client)
			if err != nil {
				if h.coordinator.shouldRetry(h.coordinator.getTimeoutDuration()) {
					time.Sleep(time.Second)
					continue
				}
				h.coordinator.writeError(err, proxy.StatusServerError)
				return
			}

			isEndlist = endlist

			if err := h.streamSegments(ctx, segments); err != nil {
				if h.coordinator.shouldRetry(h.coordinator.getTimeoutDuration()) {
					time.Sleep(time.Second)
					continue
				}
				h.coordinator.writeError(err, proxy.StatusServerError)
				return
			}

			if isEndlist {
				h.coordinator.writeError(io.EOF, proxy.StatusEOF)
				return
			}

			time.Sleep(time.Second)
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

			req, err := http.NewRequest("GET", segmentURL, nil)
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
				chunk.Reset()
				continue
			}

			chunk.Timestamp = time.Now()
			if !h.coordinator.Write(chunk) {
				chunk.Reset()
				return fmt.Errorf("failed to write chunk")
			}
		}
	}
	return nil
}

func (h *M3U8StreamHandler) fetchMediaPlaylist(mediaURL string, client *http.Client) ([]string, bool, error) {
	resp, err := client.Get(mediaURL)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, err
	}

	lines := strings.Split(string(body), "\n")
	base, _ := url.Parse(mediaURL)

	var segments []string
	isEndlist := false

	for _, line := range lines {
		line = strings.TrimSpace(line)

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
		segments = append(segments, segURL.String())
	}

	return segments, isEndlist, nil
}

func (h *M3U8StreamHandler) getBestVariant(lines []string, baseURL *url.URL) (VariantStream, error) {
	var variants []VariantStream

	for i, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			if i+1 >= len(lines) {
				continue
			}

			variant, err := h.parseVariantStream(line, lines[i+1], baseURL)
			if err != nil {
				h.logger.Errorf("Error parsing variant: %v", err)
				continue
			}
			variants = append(variants, variant)
		}
	}

	if len(variants) == 0 {
		return VariantStream{}, fmt.Errorf("no variants found")
	}

	// Choose the variant with the highest bandwidth
	best := variants[0]
	for _, v := range variants {
		if v.Bandwidth > best.Bandwidth {
			best = v
		}
	}

	return best, nil
}

func (h *M3U8StreamHandler) parseVariantStream(
	streamInf string,
	uri string,
	baseURL *url.URL,
) (VariantStream, error) {
	var variant VariantStream

	attrStr := strings.TrimPrefix(streamInf, "#EXT-X-STREAM-INF:")
	attrs := strings.Split(attrStr, ",")

	for _, attr := range attrs {
		attr = strings.TrimSpace(attr)
		if strings.HasPrefix(attr, "BANDWIDTH=") {
			bwStr := strings.TrimPrefix(attr, "BANDWIDTH=")
			bw, err := strconv.Atoi(bwStr)
			if err == nil {
				variant.Bandwidth = bw
			}
		}
	}

	uri = strings.TrimSpace(uri)
	u, err := url.Parse(uri)
	if err != nil {
		return variant, fmt.Errorf("invalid variant URL: %w", err)
	}

	if !u.IsAbs() {
		u = baseURL.ResolveReference(u)
	}
	variant.URL = u.String()

	return variant, nil
}
