package handlers

import (
	"context"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/store"
	"m3u-stream-merger/utils"
	"net/http"
	"path"
	"strings"
)

type StreamHandler struct {
	manager StreamManager
	logger  logger.Logger
}

func NewStreamHandler(manager StreamManager, logger logger.Logger) *StreamHandler {
	return &StreamHandler{
		manager: manager,
		logger:  logger,
	}
}

func (h *StreamHandler) handleExitCode(code int, resp *http.Response, r *http.Request, session *store.Session, selectedIndex, selectedSubIndex string) bool {
	switch code {
	case proxy.StatusEOF:
		if utils.EOFIsExpected(resp) {
			h.logger.Logf("Successfully proxied playlist: %s", r.RemoteAddr)
			return true
		}
		fallthrough
	case proxy.StatusServerError:
		indexes := append(session.GetTestedIndexes(), selectedIndex+"|"+selectedSubIndex)
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
		var err error
		var selectedURL, selectedIndex, selectedSubIndex string

		resp, selectedURL, selectedIndex, selectedSubIndex, err = h.manager.LoadBalancer(ctx, r, session)
		if err != nil {
			return err
		}
		if err := h.writeHeaders(w, resp, firstWrite); err != nil {
			return err
		}
		firstWrite = false

		exitStatus := make(chan int)
		h.logger.Logf("Proxying %s to %s", r.RemoteAddr, selectedURL)

		proxyCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		h.manager.GetConcurrencyManager().UpdateConcurrency(selectedIndex, true)
		go h.manager.ProxyStream(proxyCtx, resp, r, w, exitStatus)

		select {
		case <-ctx.Done():
			h.manager.GetConcurrencyManager().UpdateConcurrency(selectedIndex, false)
			h.logger.Logf("Client has closed the stream: %s", r.RemoteAddr)

			return nil
		case code := <-exitStatus:
			h.manager.GetConcurrencyManager().UpdateConcurrency(selectedIndex, false)
			if handled := h.handleExitCode(code, resp, r, session, selectedIndex, selectedSubIndex); handled {
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
