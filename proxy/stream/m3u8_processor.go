package stream

import (
	"fmt"
	"io"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/proxy"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type VariantStream struct {
	Bandwidth int
	URL       string
}

type M3U8StreamHandler struct {
	config *StreamConfig
	logger logger.Logger
	client *http.Client
}

func NewM3U8StreamHandler(config *StreamConfig, logger logger.Logger) *M3U8StreamHandler {
	return &M3U8StreamHandler{
		config: config,
		logger: logger,
		client: &http.Client{},
	}
}

func (h *M3U8StreamHandler) HandleHLSStream(
	masterResp *http.Response,
	writer ResponseWriter,
	baseURL *url.URL,
) StreamResult {
	// Check if writer supports flushing
	flusher, ok := writer.(http.Flusher)
	if !ok {
		return StreamResult{0, fmt.Errorf("streaming unsupported"), proxy.StatusServerError}
	}

	// Read the master playlist
	body, err := io.ReadAll(masterResp.Body)
	defer masterResp.Body.Close()
	if err != nil {
		return StreamResult{0, fmt.Errorf("failed to read master playlist: %w", err), proxy.StatusServerError}
	}

	var bytesWritten int64
	lines := strings.Split(string(body), "\n")

	// Check if this is a master playlist or media playlist
	isMaster := false
	for _, line := range lines {
		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			isMaster = true
			break
		}
	}

	var mediaPlaylistURL string
	if isMaster {
		// Parse variants and get the best one
		bestVariant, err := h.getBestVariant(lines, baseURL)
		if err != nil {
			return StreamResult{0, fmt.Errorf("failed to get best variant: %w", err), proxy.StatusServerError}
		}
		mediaPlaylistURL = bestVariant.URL
	} else {
		mediaPlaylistURL = baseURL.String()
	}

	h.logger.Debugf("Selected media playlist: %s", mediaPlaylistURL)

	// Start streaming segments
	for {
		// Fetch media playlist
		segments, err := h.fetchMediaPlaylist(mediaPlaylistURL)
		if err != nil {
			return StreamResult{bytesWritten, fmt.Errorf("failed to fetch media playlist: %w", err), proxy.StatusServerError}
		}

		// Stream each segment
		for _, segmentURL := range segments {
			h.logger.Debugf("Fetching segment: %s", segmentURL)

			req, err := http.NewRequest("GET", segmentURL, nil)
			if err != nil {
				h.logger.Errorf("Error creating segment request: %v", err)
				continue
			}

			segResp, err := h.client.Do(req)
			if err != nil {
				h.logger.Errorf("Error fetching segment: %v", err)
				continue
			}

			n, err := io.Copy(writer, segResp.Body)
			segResp.Body.Close()

			if err != nil {
				return StreamResult{bytesWritten, fmt.Errorf("error streaming segment: %w", err), proxy.StatusClientClosed}
			}

			bytesWritten += n
			flusher.Flush()
		}

		// Brief pause before fetching the next playlist
		time.Sleep(time.Second * 2)
	}
}

func (h *M3U8StreamHandler) getBestVariant(lines []string, baseURL *url.URL) (VariantStream, error) {
	var variants []VariantStream
	var streamInfLine string

	for i, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			streamInfLine = line
			if i+1 < len(lines) {
				variant, err := h.parseVariantStream(streamInfLine, lines[i+1], baseURL)
				if err != nil {
					h.logger.Errorf("Error parsing variant: %v", err)
					continue
				}
				variants = append(variants, variant)
			}
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

	// Parse bandwidth from STREAM-INF
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

	// Parse and resolve URI
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

func (h *M3U8StreamHandler) fetchMediaPlaylist(mediaURL string) ([]string, error) {
	resp, err := h.client.Get(mediaURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(body), "\n")
	base, _ := url.Parse(mediaURL)

	var segments []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Resolve relative URLs
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

	return segments, nil
}
