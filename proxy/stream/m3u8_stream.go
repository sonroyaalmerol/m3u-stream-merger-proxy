package stream

import (
	"bufio"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/proxy/loadbalancer"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/patrickmn/go-cache"
)

var httpClient = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:       100,
		IdleConnTimeout:    90 * time.Second,
		DisableCompression: true,
		MaxConnsPerHost:    100,
		DisableKeepAlives:  false,
	},
}

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

	h.coordinator.writerCtxMu.Lock()
	isFirstClient := atomic.LoadInt32(&h.coordinator.clientCount) == 0
	if isFirstClient {
		h.coordinator.writerCtx, h.coordinator.writerCancel = context.WithCancel(context.Background())
		go h.startHLSWriter(h.coordinator.writerCtx, lbResult)
	}
	h.coordinator.writerCtxMu.Unlock()

	if err := h.coordinator.RegisterClient(); err != nil {
		return StreamResult{0, err, proxy.StatusServerError}
	}
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

	client := httpClient // Use the optimized client we created earlier
	mediaURL := lbResult.Response.Request.URL.String()

	// Create a TTL cache for processed segments
	processedSegmentsCache := cache.New(60*time.Second, 10*time.Second)

	// Start with a default polling interval
	pollInterval := time.Second
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	var lastMediaSequence int64 = -1

	for {
		select {
		case <-ctx.Done():
			h.logger.Debug("Context cancelled, stopping HLS writer")
			h.coordinator.writeError(ctx.Err(), proxy.StatusClientClosed)
			return
		case <-ticker.C:
			reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			metadata, err := h.fetchPlaylistMetadata(reqCtx, mediaURL, client)
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

			// Update polling interval based on target duration
			if metadata.TargetDuration > 0 {
				newInterval := time.Duration(metadata.TargetDuration * float64(time.Second) / 2)
				if newInterval != pollInterval {
					h.logger.Debugf("Updating polling interval from %v to %v", pollInterval, newInterval)
					pollInterval = newInterval
					ticker.Reset(pollInterval)
				}
			}

			// Process new segments
			if metadata.MediaSequence > lastMediaSequence || lastMediaSequence == -1 {
				newSegments := make([]string, 0)
				for _, segment := range metadata.Segments {
					if _, found := processedSegmentsCache.Get(segment); !found {
						newSegments = append(newSegments, segment)
						processedSegmentsCache.Set(segment, true, cache.DefaultExpiration)
					}
				}

				if len(newSegments) > 0 {
					if err := h.streamSegments(ctx, newSegments); err != nil {
						if ctx.Err() != nil {
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
				lastMediaSequence = metadata.MediaSequence
			}

			if metadata.IsEndlist {
				h.logger.Debug("Playlist ended, stopping HLS writer")
				h.coordinator.writeError(io.EOF, proxy.StatusEOF)
				return
			}
		}
	}
}

func (h *M3U8StreamHandler) streamSegments(ctx context.Context, segments []string) error {
	// Use semaphore to limit concurrent downloads
	sem := make(chan struct{}, 3) // Limit to 3 concurrent downloads
	var wg sync.WaitGroup
	var errOnce sync.Once
	var streamErr error

	for _, segmentURL := range segments {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			wg.Add(1)
			sem <- struct{}{} // Acquire semaphore

			go func(url string) {
				defer func() {
					<-sem // Release semaphore
					wg.Done()
				}()

				chunk := newChunkData()
				defer chunk.Reset() // Ensure chunk is always reset

				req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
				if err != nil {
					errOnce.Do(func() {
						streamErr = fmt.Errorf("error creating request: %w", err)
					})
					return
				}

				resp, err := httpClient.Do(req)
				if err != nil {
					h.logger.Errorf("Error fetching segment %s: %v", url, err)
					return
				}
				defer resp.Body.Close()

				// Use limited reader to prevent memory exhaustion
				limitedReader := io.LimitReader(resp.Body, int64(h.config.ChunkSize))

				_, err = io.Copy(chunk.Buffer, limitedReader)
				if err != nil {
					h.logger.Errorf("Error copying segment data: %v", err)
					return
				}

				chunk.Timestamp = time.Now()
				if !h.coordinator.Write(chunk) {
					errOnce.Do(func() {
						streamErr = fmt.Errorf("failed to write chunk")
					})
					return
				}
			}(segmentURL)
		}
	}

	wg.Wait() // Wait for all segments to complete
	return streamErr
}

func (h *M3U8StreamHandler) fetchPlaylistMetadata(ctx context.Context, mediaURL string, client *http.Client) (*PlaylistMetadata, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", mediaURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Add Accept-Encoding header to handle gzip
	req.Header.Set("Accept-Encoding", "gzip")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch playlist: %w", err)
	}
	defer resp.Body.Close()

	// Handle gzipped response
	var reader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gzReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to create gzip reader: %w", err)
		}
		defer gzReader.Close()
		reader = gzReader
	}

	// Use limited reader to prevent memory exhaustion
	limitedReader := io.LimitReader(reader, 1024*1024)
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
