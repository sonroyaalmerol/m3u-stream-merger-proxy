package handlers

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"m3u-stream-merger/logger"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/proxy/client"
	"m3u-stream-merger/proxy/stream/failovers"
	"m3u-stream-merger/utils"
)

type StreamHTTPHandler struct {
	manager ProxyInstance
	logger  logger.Logger
}

func NewStreamHTTPHandler(manager ProxyInstance, logger logger.Logger) *StreamHTTPHandler {
	return &StreamHTTPHandler{
		manager: manager,
		logger:  logger,
	}
}

func (h *StreamHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	streamClient := client.NewStreamClient(w, r)

	h.handleStream(r.Context(), streamClient)
}

func (h *StreamHTTPHandler) ServeSegmentHTTP(w http.ResponseWriter, r *http.Request) {
	streamClient := client.NewStreamClient(w, r)

	h.handleSegmentStream(streamClient)
}

func (h *StreamHTTPHandler) extractStreamURL(urlPath string) string {
	base := path.Base(urlPath)
	parts := strings.Split(base, ".")
	if len(parts) == 0 {
		return ""
	}
	return strings.TrimPrefix(parts[0], "/")
}

func (h *StreamHTTPHandler) handleStream(ctx context.Context, streamClient *client.StreamClient) {
	r := streamClient.Request

	streamURL := h.extractStreamURL(r.URL.Path)
	if streamURL == "" {
		h.logger.Logf("Invalid m3uID for request from %s: %s", r.RemoteAddr, r.URL.Path)
		return
	}

	coordinator := h.manager.GetStreamRegistry().GetOrCreateCoordinator(streamURL)

	for {
		lbResult := coordinator.GetWriterLBResult()
		var err error
		if lbResult == nil {
			h.logger.Debugf("No existing shared buffer found for %s", streamURL)
			h.logger.Debugf("Client %s executing load balancer.", r.RemoteAddr)
			lbResult, err = h.manager.LoadBalancer(ctx, r)
			if err != nil {
				h.logger.Logf("Load balancer error (%s): %v", r.URL.Path, err)
				return
			}
		} else {
			if _, ok := h.manager.GetConcurrencyManager().Invalid.Load(lbResult.URL); ok {
				return
			}
			h.logger.Logf("Existing shared buffer found for %s", streamURL)
		}

		exitStatus := make(chan int)
		h.logger.Logf("Proxying %s to %s", r.URL.Path, lbResult.URL)

		proxyCtx, cancel := context.WithCancel(ctx)
		go func() {
			defer cancel()
			h.manager.ProxyStream(proxyCtx, coordinator, lbResult, streamClient, exitStatus)
		}()

		select {
		case <-ctx.Done():
			h.logger.Logf("Client has closed the stream: %s", r.RemoteAddr)
			return
		case code := <-exitStatus:
			if h.handleExitCode(code, r) {
				return
			}
			// Otherwise, retry with a new lbResult.
		}

		select {
		case <-ctx.Done():
			h.logger.Logf("Client has closed the stream: %s", r.RemoteAddr)
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (h *StreamHTTPHandler) handleExitCode(code int, r *http.Request) bool {
	switch code {
	case proxy.StatusIncompatible:
		h.logger.Errorf("Finished handling M3U8 %s request but failed to parse contents.",
			r.Method, r.RemoteAddr)
		fallthrough
	case proxy.StatusEOF:
		fallthrough
	case proxy.StatusServerError:
		h.logger.Logf("Retrying other servers...")
		return false
	case proxy.StatusM3U8Parsed:
		h.logger.Debugf("Finished handling M3U8 %s request: %s", r.Method,
			r.RemoteAddr)
		return true
	case proxy.StatusM3U8ParseError:
		h.logger.Errorf("Finished handling M3U8 %s request but failed to parse contents.",
			r.Method, r.RemoteAddr)
		return false
	default:
		h.logger.Logf("Unable to write to client. Assuming stream has been closed: %s",
			r.RemoteAddr)
		return true
	}
}

func (h *StreamHTTPHandler) handleSegmentStream(streamClient *client.StreamClient) {
	r := streamClient.Request

	h.logger.Debugf("Received request from %s for URL: %s",
		r.RemoteAddr, r.URL.Path)

	streamId := h.extractStreamURL(r.URL.Path)
	if streamId == "" {
		h.logger.Errorf("Invalid m3uID for request from %s: %s",
			r.RemoteAddr, r.URL.Path)
		return
	}

	segment, err := failovers.ParseSegmentId(streamId)
	if err != nil {
		h.logger.Errorf("Segment parsing error %s: %s",
			r.RemoteAddr, r.URL.Path)
		_ = streamClient.WriteHeader(http.StatusInternalServerError)
		_, _ = streamClient.Write([]byte(fmt.Sprintf("Segment parsing error: %v", err)))
		return
	}

	resp, err := utils.HTTPClient.Get(segment.URL)
	if err != nil {
		h.logger.Errorf("Failed to fetch URL: %v", err)
		_ = streamClient.WriteHeader(http.StatusInternalServerError)
		_, _ = streamClient.Write([]byte(fmt.Sprintf("Failed to fetch URL: %v", err)))
		return
	}
	defer resp.Body.Close()

	for key, values := range resp.Header {
		for _, value := range values {
			streamClient.Header().Add(key, value)
		}
	}

	_ = streamClient.WriteHeader(resp.StatusCode)

	if _, err = io.Copy(streamClient.GetWriter(), resp.Body); err != nil {
		if isBrokenPipe(err) {
			h.logger.Debugf("Client disconnected (broken pipe): %v", err)
		} else {
			h.logger.Errorf("Error copying response body: %v", err)
		}
	}
}

func isBrokenPipe(err error) bool {
	if err == nil {
		return false
	}

	if opErr, ok := err.(*net.OpError); ok {
		if sysErr, ok := opErr.Err.(*os.SyscallError); ok {
			errMsg := sysErr.Err.Error()
			return strings.Contains(errMsg, "broken pipe") ||
				strings.Contains(errMsg, "connection reset by peer")
		}
		errMsg := opErr.Err.Error()
		return strings.Contains(errMsg, "broken pipe") ||
			strings.Contains(errMsg, "connection reset by peer")
	}

	return strings.Contains(err.Error(), "broken pipe") ||
		strings.Contains(err.Error(), "connection reset by peer")
}
