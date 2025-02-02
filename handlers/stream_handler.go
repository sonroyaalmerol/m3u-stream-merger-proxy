package handlers

import (
	"context"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/proxy/loadbalancer"
	"m3u-stream-merger/proxy/stream"
	"m3u-stream-merger/store"
	"m3u-stream-merger/utils"
	"net/http"
	"path"
	"strings"
)

type StreamHandler struct {
	manager     StreamManager
	logger      logger.Logger
	coordinator *stream.StreamCoordinator
}

func NewStreamHandler(manager StreamManager, logger logger.Logger) *StreamHandler {
	return &StreamHandler{
		manager: manager,
		logger:  logger,
	}
}

func (h *StreamHandler) handleExitCode(code int, lbResult *loadbalancer.LoadBalancerResult, r *http.Request, session *store.Session) bool {
	switch code {
	case proxy.StatusEOF:
		if utils.EOFIsExpected(lbResult.Response) {
			h.logger.Logf("Successfully proxied playlist: %s", r.RemoteAddr)
			return true
		}
		fallthrough
	case proxy.StatusServerError:
		indexes := append(session.GetTestedIndexes(), lbResult.Index+"|"+lbResult.SubIndex)
		session.SetTestedIndexes(indexes)
		h.logger.Logf("Retrying other servers...")
		return false
	case proxy.StatusM3U8Parsed:
		h.logger.Logf("Finished handling M3U8 %s request: %s", r.Method, r.RemoteAddr)
		return true
	case proxy.StatusM3U8ParseError:
		h.logger.Errorf("Finished handling M3U8 %s request but failed to parse contents.", r.Method, r.RemoteAddr)
		return false
	default:
		h.logger.Logf("Unable to write to client. Assuming stream has been closed: %s", r.RemoteAddr)
		return true
	}
}

func (h *StreamHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.logger.Logf("Received request from %s for URL: %s", r.RemoteAddr, r.URL.Path)

	streamURL := h.extractStreamURL(r.URL.Path)
	if streamURL == "" {
		h.logger.Logf("Invalid m3uID for request from %s: %s", r.RemoteAddr, r.URL.Path)
		http.NotFound(w, r)
		return
	}

	h.coordinator = h.manager.GetStreamRegistry().GetOrCreateCoordinator(streamURL)

	if err := h.handleStream(r.Context(), w, r); err != nil {
		h.logger.Logf("Error handling stream %s: %v", streamURL, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

func (h *StreamHandler) extractStreamURL(urlPath string) string {
	base := path.Base(urlPath)
	parts := strings.Split(base, ".")
	if len(parts) == 0 {
		return ""
	}
	return strings.TrimPrefix(parts[0], "/")
}

func (h *StreamHandler) handleStream(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	session := store.GetOrCreateSession(r)
	firstWrite := true
	var resp *http.Response

	defer func() {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
	}()

	for {
		var lbResults *loadbalancer.LoadBalancerResult
		var err error

		if lbResults = h.coordinator.GetWriterLBResult(); lbResults == nil {
			lbResults, err = h.manager.LoadBalancer(ctx, r, session)
			if err != nil {
				return err
			}
		}

		resp = lbResults.Response

		if err := h.writeHeaders(w, resp, firstWrite); err != nil {
			return err
		}
		firstWrite = false

		exitStatus := make(chan int)
		h.logger.Logf("Proxying %s to %s", r.RemoteAddr, lbResults.URL)

		proxyCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		go h.manager.ProxyStream(proxyCtx, h.coordinator, lbResults, r, w, exitStatus)

		select {
		case <-ctx.Done():
			h.logger.Logf("Client has closed the stream: %s", r.RemoteAddr)

			return nil
		case code := <-exitStatus:
			if handled := h.handleExitCode(code, lbResults, r, session); handled {
				return nil
			}
			// Continue to retry if not handled
		}
	}
}

func (h *StreamHandler) writeHeaders(w http.ResponseWriter, resp *http.Response, firstWrite bool) error {
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
