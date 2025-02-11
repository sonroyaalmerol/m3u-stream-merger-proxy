package handlers

import (
	"context"
	"net/http"
	"path"
	"strings"

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
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
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
			lbResult, err = h.manager.LoadBalancer(ctx, r, session)
			if err != nil {
				return err
			}
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
	}
}

func (h *StreamHTTPHandler) writeHeaders(w http.ResponseWriter, resp *http.Response,
	firstWrite bool) error {
	if !firstWrite {
		return nil
	}

	for k, v := range resp.Header {
		if strings.ToLower(k) == "content-length" {
			continue
		}
		for _, val := range v {
			w.Header().Set(k, val)
		}
	}

	w.WriteHeader(resp.StatusCode)
	h.logger.Debugf("Headers set for response: %v", w.Header())
	return nil
}

func (h *StreamHTTPHandler) handleExitCode(code int,
	lbResult *loadbalancer.LoadBalancerResult, r *http.Request,
	session *store.Session) bool {
	switch code {
	case proxy.StatusEOF:
		if utils.EOFIsExpected(lbResult.Response) {
			h.logger.Logf("Successfully proxied playlist: %s", r.RemoteAddr)
			return true
		}
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
