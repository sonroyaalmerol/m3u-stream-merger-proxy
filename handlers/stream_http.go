package handlers

import (
	"context"
	"net/http"
	"path"
	"strings"
	"time"

	"m3u-stream-merger/logger"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/proxy/loadbalancer"
	"m3u-stream-merger/store"
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
	if err := h.handleStream(r.Context(), w, r); err != nil {
		h.logger.Logf("Error handling stream: %v", err)
		_, _ = w.Write([]byte{})
	}
}

func (h *StreamHTTPHandler) extractStreamURL(urlPath string) string {
	base := path.Base(urlPath)
	parts := strings.Split(base, ".")
	if len(parts) == 0 {
		return ""
	}
	return strings.TrimPrefix(parts[0], "/")
}

func (h *StreamHTTPHandler) handleStream(ctx context.Context, w http.ResponseWriter,
	r *http.Request) error {
	h.logger.Logf("Received request from %s for URL: %s", r.RemoteAddr, r.URL.Path)
	streamURL := h.extractStreamURL(r.URL.Path)
	if streamURL == "" {
		h.logger.Logf("Invalid m3uID for request from %s: %s",
			r.RemoteAddr, r.URL.Path)
		http.NotFound(w, r)
	}

	session := store.GetOrCreateSession(r)
	firstWrite := true

	coordinator := h.manager.GetStreamRegistry().GetOrCreateCoordinator(streamURL)

	for {
		lbResult := coordinator.GetWriterLBResult()
		var err error
		if lbResult == nil {
			h.logger.Logf("No existing shared buffer found for %s", streamURL)
			h.logger.Logf("Client %s executing load balancer.", r.RemoteAddr)
			lbResult, err = h.manager.LoadBalancer(ctx, r, session)
			if err != nil {
				return err
			}
		} else {
			h.logger.Logf("Existing shared buffer found for %s", streamURL)
		}

		resp := lbResult.Response
		if err := h.writeHeaders(w, resp, firstWrite); err != nil {
			return err
		}
		firstWrite = false

		exitStatus := make(chan int)
		h.logger.Logf("Proxying %s to %s", r.RemoteAddr, lbResult.URL)

		proxyCtx, cancel := context.WithCancel(ctx)
		go func() {
			defer cancel()
			h.manager.ProxyStream(proxyCtx, coordinator, lbResult, r, w,
				exitStatus)
		}()

		select {
		case <-ctx.Done():
			h.logger.Logf("Client has closed the stream: %s", r.RemoteAddr)
			return nil
		case code := <-exitStatus:
			if h.handleExitCode(code, lbResult, r, session) {
				return nil
			}
			// Otherwise, retry with a new lbResult.
		}

		select {
		case <-ctx.Done():
			h.logger.Logf("Client has closed the stream: %s", r.RemoteAddr)
			return nil
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (h *StreamHTTPHandler) writeHeaders(w http.ResponseWriter, resp *http.Response,
	firstWrite bool) error {
	if !firstWrite {
		return nil
	}

	isM3u8 := utils.IsAnM3U8Media(resp)

	for k, v := range resp.Header {
		if strings.ToLower(k) == "content-length" || (strings.ToLower(k) == "content-type" && isM3u8) {
			continue
		}
		for _, val := range v {
			w.Header().Set(k, val)
		}
	}

	h.logger.Debugf("Headers set for response: %v", w.Header())
	return nil
}

func (h *StreamHTTPHandler) handleExitCode(code int,
	lbResult *loadbalancer.LoadBalancerResult, r *http.Request,
	session *store.Session) bool {
	switch code {
	case proxy.StatusIncompatible:
		session.AddInvalidIndex(lbResult.Index + "|" + lbResult.SubIndex)
		fallthrough
	case proxy.StatusEOF:
		fallthrough
	case proxy.StatusServerError:
		index := lbResult.Index + "|" + lbResult.SubIndex
		session.SetTestedIndexes(append(session.GetTestedIndexes(), index))
		h.logger.Logf("Retrying other servers...")
		return false
	case proxy.StatusM3U8Parsed:
		h.logger.Logf("Finished handling M3U8 %s request: %s", r.Method,
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
