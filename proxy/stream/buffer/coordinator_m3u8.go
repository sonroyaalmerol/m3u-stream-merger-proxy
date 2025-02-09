package buffer

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/proxy/loadbalancer"
	"m3u-stream-merger/utils"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
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

type PlaylistMetadata struct {
	TargetDuration float64
	MediaSequence  int64
	Version        int
	IsEndlist      bool
	Segments       []string
	IsMaster       bool
}

func (c *StreamCoordinator) StartHLSWriter(ctx context.Context, lbResult *loadbalancer.LoadBalancerResult, writer http.ResponseWriter) {
	defer func() {
		c.LBResultOnWrite.Store(nil)
		if r := recover(); r != nil {
			c.logger.Errorf("Panic in StartHLSWriter: %v", r)
			c.writeError(fmt.Errorf("internal server error"), proxy.StatusServerError)
		}
	}()

	if !c.WriterActive.CompareAndSwap(false, true) {
		c.logger.Warn("Writer already active, aborting start")
		return
	}
	defer c.WriterActive.Store(false)

	c.LBResultOnWrite.Store(lbResult)
	c.logger.Debug("StartHLSWriter: Beginning read loop")

	c.cm.UpdateConcurrency(lbResult.Index, true)
	defer c.cm.UpdateConcurrency(lbResult.Index, false)

	var lastErr error
	lastChangeTime := time.Now()
	lastMediaSeq := int64(-1)

	// Start with a conservative polling rate
	pollInterval := time.Second
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for atomic.LoadInt32(&c.state) == stateActive {
		select {
		case <-ctx.Done():
			if lastErr == nil {
				c.logger.Debug("StartHLSWriter: Context cancelled")
				c.writeError(ctx.Err(), proxy.StatusClientClosed)
			}
			return
		case <-ticker.C:
			// Check timeout first
			if time.Since(lastChangeTime) > time.Duration(c.config.TimeoutSeconds)*time.Second+pollInterval {
				c.logger.Debug("No sequence changes detected within timeout period")
				c.writeError(fmt.Errorf("stream timeout: no new segments"), proxy.StatusEOF)
				return
			}
			m3uPlaylist, err := io.ReadAll(lbResult.Response.Body)
			lbResult.Response.Body.Close()
			if err != nil {
				c.writeError(err, proxy.StatusServerError)
				return
			}

			mediaURL := lbResult.Response.Request.URL.String()
			metadata, err := c.parsePlaylist(mediaURL, string(m3uPlaylist))
			if err != nil {
				c.writeError(err, proxy.StatusServerError)
				return
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
					c.logger.Debugf("Updated polling interval to %v", pollInterval)
				}
			}

			if metadata.IsMaster {
				c.writeError(fmt.Errorf("master playlist not supported"), proxy.StatusServerError)
				return
			}

			if metadata.IsEndlist {
				// Process remaining segments before ending
				err = c.processSegments(ctx, metadata.Segments, writer)
				if err != nil {
					c.logger.Errorf("Error processing segments: %v", err)
				}
				c.writeError(io.EOF, proxy.StatusEOF)
				return
			}

			if metadata.MediaSequence > lastMediaSeq {
				lastChangeTime = time.Now()
				lastMediaSeq = metadata.MediaSequence

				if err := c.processSegments(ctx, metadata.Segments, writer); err != nil {
					if ctx.Err() != nil {
						c.writeError(err, proxy.StatusServerError)
						return
					}
					lastErr = err
				}
			}

		}
	}
}

func (c *StreamCoordinator) processSegments(ctx context.Context, segments []string, writer http.ResponseWriter) error {
	for _, segment := range segments {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			if err := c.streamSegment(ctx, segment, writer); err != nil {
				if err != io.EOF {
					return err
				}
			}
		}
	}
	return nil
}

func (c *StreamCoordinator) streamSegment(ctx context.Context, segmentURL string, writer http.ResponseWriter) error {
	buffer := make([]byte, c.config.ChunkSize)
	timeout := c.getTimeoutDuration()
	backoff := proxy.NewBackoffStrategy(c.config.InitialBackoff,
		time.Duration(c.config.TimeoutSeconds-1)*time.Second)

	req, err := http.NewRequest("GET", segmentURL, nil)
	if err != nil {
		return fmt.Errorf("Error creating request to segment: %v", err)
	}

	resp, err := utils.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("Error fetching segment stream: %v", err)
	}

	if resp == nil {
		return errors.New("Returned nil response from HTTP client")
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Non-200 status code received: %d for %s", resp.StatusCode, segmentURL)
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "video/MP2T" // Default to MPEG-TS if we can't detect
	}
	c.logger.Debugf("Detected segment content type: %s", contentType)

	if !safeConcatTypes[strings.ToLower(contentType)] {
		c.WasInvalid.Store(true)
		c.logger.Errorf("%s cannot be safely concatenated and is not supported by this proxy.", contentType)
		return fmt.Errorf("content type %s cannot be safely concatenated", contentType)
	}

	if !c.WrittenHeader.Load() {
		writer.Header().Add("Content-Type", contentType)
		writer.WriteHeader(resp.StatusCode)
		c.WrittenHeader.Store(true)
	}

	lastSuccess := time.Now()
	lastErr := time.Now()
	zeroReads := 0

	for atomic.LoadInt32(&c.state) == stateActive {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			if c.shouldTimeout(lastSuccess, timeout) {
				return nil
			}

			n, err := resp.Body.Read(buffer)
			c.logger.Debugf("StartHLSWriter: Read %d bytes, err: %v", n, err)

			if n == 0 {
				zeroReads++
				if zeroReads > 10 {
					return io.EOF
				}
				time.Sleep(10 * time.Millisecond)
				continue
			}

			lastSuccess = time.Now()
			zeroReads = 0

			if err == io.EOF {
				if n > 0 {
					chunk := newChunkData()
					_, _ = chunk.Buffer.Write(buffer[:n])
					chunk.Timestamp = time.Now()
					if !c.Write(chunk) {
						chunk.Reset()
					}
				}
				return io.EOF
			}

			if err != nil {
				if c.shouldRetry(timeout) {
					backoff.Sleep(ctx)
					lastErr = time.Now()
					continue
				}
				return err
			}

			chunk := newChunkData()
			_, _ = chunk.Buffer.Write(buffer[:n])
			chunk.Timestamp = time.Now()
			if !c.Write(chunk) {
				chunk.Reset()
			}

			// Only reset the backoff if at least one second has passed
			if time.Since(lastErr) >= time.Second {
				backoff.Reset()
				lastErr = time.Now()
			}
		}
	}

	return nil
}

func (c *StreamCoordinator) parsePlaylist(mediaURL string, content string) (*PlaylistMetadata, error) {
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
				c.logger.Warnf("Invalid segment URL %q: %v", line, err)
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
