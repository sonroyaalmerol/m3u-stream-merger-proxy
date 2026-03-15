package buffer

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/proxy/client"
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

const (
	initialSegmentCap = 32
)

type PlaylistMetadata struct {
	TargetDuration float64
	MediaSequence  int64
	Version        int
	IsEndlist      bool
	Segments       []string
	IsMaster       bool
}

func (c *StreamCoordinator) StartHLSWriter(ctx context.Context, lbResult *loadbalancer.LoadBalancerResult, streamC *client.StreamClient) {
	defer func() {
		c.LBResultOnWrite.Store(nil)
		if r := recover(); r != nil {
			c.logger.Errorf("Panic in StartHLSWriter: %v", r)
			c.writeError(fmt.Errorf("internal server error"), proxy.StatusServerError)
		}
	}()

	c.LBResultOnWrite.Store(lbResult)
	c.WriterRespHeader.Store(nil)

	newHeaderChan := make(chan struct{})
	c.respHeaderSet.Store(&newHeaderChan)
	c.m3uHeaderSet.Store(false)
	c.logger.Debug("StartHLSWriter: Beginning read loop")

	if !c.cm.UpdateConcurrency(lbResult.Index, true) {
		c.logger.Warnf("Failed to acquire concurrency slot for M3U_%s", lbResult.Index)
		c.writeError(fmt.Errorf("concurrency limit reached"), proxy.StatusServerError)
		return
	}
	defer c.cm.UpdateConcurrency(lbResult.Index, false)

	playlistURL := lbResult.Response.Request.URL.String()
	lbResult.Response.Body.Close()

	var lastErr error
	lastChangeTime := time.Now()
	pollInterval := time.Second
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	writeErrorAndReturn := func(err error, status int) {
		ticker.Stop()
		c.writeError(err, status)
	}

	lastMediaSeq := c.lastProcessedSeq.Load()

	for atomic.LoadInt32(&c.state) == stateActive {
		select {
		case <-ctx.Done():
			if lastErr == nil {
				c.logger.Debug("StartHLSWriter: Context cancelled")
				writeErrorAndReturn(ctx.Err(), proxy.StatusClientClosed)
			}
			return

		case <-ticker.C:
			timeout := time.Duration(c.config.TimeoutSeconds)*time.Second + pollInterval
			if time.Since(lastChangeTime) > timeout {
				c.logger.Debug("No sequence changes detected within timeout period")
				writeErrorAndReturn(ErrStreamTimeout, proxy.StatusEOF)
				return
			}

			resp, err := utils.CustomHttpRequest(streamC.Request, "GET", playlistURL)
			if err != nil {
				c.logger.Warnf("Failed to fetch playlist: %v", err)
				lastErr = err
				continue // Retry on next tick
			}

			if resp == nil {
				c.logger.Warn("Received nil response from HTTP client")
				continue
			}

			if resp.StatusCode != http.StatusOK {
				resp.Body.Close()
				c.logger.Warnf("Non-200 status for playlist: %d", resp.StatusCode)
				lastErr = fmt.Errorf("playlist returned status %d", resp.StatusCode)
				continue
			}

			m3uPlaylist, err := io.ReadAll(resp.Body)
			resp.Body.Close()

			if err != nil {
				c.logger.Warnf("Failed to read playlist body: %v", err)
				lastErr = err
				continue
			}

			metadata, err := c.parsePlaylist(playlistURL, string(m3uPlaylist))
			if err != nil {
				c.logger.Warnf("Failed to parse playlist: %v", err)
				lastErr = err
				continue
			}

			if metadata.TargetDuration > 0 {
				newInterval := time.Duration(metadata.TargetDuration * float64(time.Second) / 2)
				jitter := time.Duration(float64(newInterval) * (0.9 + 0.2*rand.Float64()))

				if math.Abs(float64(jitter-pollInterval)) > float64(pollInterval)*0.1 {
					pollInterval = jitter
					ticker.Reset(pollInterval)
					c.logger.Debugf("Updated polling interval to %v", pollInterval)
				}
			}

			if metadata.IsMaster {
				writeErrorAndReturn(fmt.Errorf("master playlist not supported"), proxy.StatusServerError)
				return
			}

			if metadata.IsEndlist {
				err := c.processSegments(ctx, metadata.Segments, streamC)
				if err != nil {
					c.logger.Errorf("Error processing segments: %v", err)
				}
				writeErrorAndReturn(io.EOF, proxy.StatusEOF)
				return
			}

			if metadata.MediaSequence > lastMediaSeq {
				lastChangeTime = time.Now()
				newSegments := c.getNewSegments(metadata.Segments, lastMediaSeq, metadata.MediaSequence)

				if err := c.processSegments(ctx, newSegments, streamC); err != nil {
					if ctx.Err() != nil {
						writeErrorAndReturn(err, proxy.StatusServerError)
						return
					}
					lastErr = err
				} else {
					lastMediaSeq = metadata.MediaSequence
					c.lastProcessedSeq.Store(lastMediaSeq)
					lastErr = nil // Clear error on success
				}
			}
		}
	}
}

func (c *StreamCoordinator) getNewSegments(allSegments []string, lastSeq, currentSeq int64) []string {
	if lastSeq < 0 {
		return allSegments
	}

	segmentCount := int64(len(allSegments))
	seqDiff := currentSeq - lastSeq

	if seqDiff <= 0 || seqDiff >= segmentCount {
		return allSegments
	}

	skipCount := max(segmentCount-seqDiff, 0)

	return allSegments[skipCount:]
}

func (c *StreamCoordinator) processSegments(ctx context.Context, segments []string, streamC *client.StreamClient) error {
	for _, segment := range segments {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			if err := c.streamSegment(ctx, segment, streamC); err != nil {
				if err != io.EOF {
					return err
				}
			}
		}
	}
	return nil
}

func (c *StreamCoordinator) streamSegment(ctx context.Context, segmentURL string, streamC *client.StreamClient) error {
	resp, err := utils.CustomHttpRequest(streamC.Request, "GET", segmentURL)
	if err != nil {
		return fmt.Errorf("Error fetching segment stream: %v", err)
	}

	if resp == nil {
		return errors.New("Returned nil response from HTTP client")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Non-200 status code received: %d for %s", resp.StatusCode, segmentURL)
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		resp.Header.Set("Content-Type", "video/MP2T")
	}

	if c.m3uHeaderSet.CompareAndSwap(false, true) {
		resp.Header.Del("Content-Length")
		c.WriterRespHeader.Store(&resp.Header)

		if ch := c.respHeaderSet.Load(); ch != nil {
			close(*ch)
		}
	}

	return c.readAndWriteStream(ctx, resp.Body, func(b []byte) error {
		c.Write(&ChunkData{
			Data:      append([]byte(nil), b...),
			Timestamp: time.Now(),
		})
		return nil
	})

}

func (c *StreamCoordinator) parsePlaylist(mediaURL string, content string) (*PlaylistMetadata, error) {
	metadata := &PlaylistMetadata{
		Segments:       make([]string, 0, initialSegmentCap),
		TargetDuration: 2,
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
