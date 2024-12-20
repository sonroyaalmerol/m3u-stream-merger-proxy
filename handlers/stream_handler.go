package handlers

import (
	"context"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/store"
	"m3u-stream-merger/utils"
	"net/http"
	"os"
	"path"
	"strings"
)

func StreamHandler(w http.ResponseWriter, r *http.Request, cm *store.ConcurrencyManager) {
	debug := os.Getenv("DEBUG") == "true"

	ctx := r.Context()

	utils.SafeLogf("Received request from %s for URL: %s\n", r.RemoteAddr, r.URL.Path)

	streamUrl := strings.Split(path.Base(r.URL.Path), ".")[0]
	if streamUrl == "" {
		utils.SafeLogf("Invalid m3uID for request from %s: %s\n", r.RemoteAddr, r.URL.Path)
		http.NotFound(w, r)
		return
	}

	stream, err := proxy.NewStreamInstance(strings.TrimPrefix(streamUrl, "/"), cm)
	if err != nil {
		utils.SafeLogf("Error retrieving stream for slug %s: %v\n", streamUrl, err)
		http.NotFound(w, r)
		return
	}

	var selectedIndex int
	var selectedUrl string

	session := store.GetOrCreateSession(r)
	firstWrite := true

	var resp *http.Response
	defer func() {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
	}()

	for {
		resp, selectedUrl, selectedIndex, err = stream.LoadBalancer(ctx, &session, r.Method)
		if err != nil {
			utils.SafeLogf("Error reloading stream for %s: %v\n", streamUrl, err)
			return
		}

		// HTTP header initialization
		if firstWrite {
			for k, v := range resp.Header {
				if strings.ToLower(k) == "content-length" {
					continue
				}

				for _, val := range v {
					w.Header().Set(k, val)
				}
			}
			w.WriteHeader(resp.StatusCode)

			if debug {
				utils.SafeLogf("[DEBUG] Headers set for response: %v\n", w.Header())
			}
			firstWrite = false
		}

		exitStatus := make(chan int)

		utils.SafeLogf("Proxying %s to %s\n", r.RemoteAddr, selectedUrl)
		proxyCtx, proxyCtxCancel := context.WithCancel(ctx)
		defer proxyCtxCancel()

		go stream.ProxyStream(proxyCtx, selectedIndex, resp, r, w, exitStatus)

		select {
		case <-ctx.Done():
			utils.SafeLogf("Client has closed the stream: %s\n", r.RemoteAddr)
			return
		case streamExitCode := <-exitStatus:
			utils.SafeLogf("Exit code %d received from %s\n", streamExitCode, selectedUrl)

			if streamExitCode == 2 && utils.EOFIsExpected(resp) {
				utils.SafeLogf("Successfully proxied playlist: %s\n", r.RemoteAddr)
				return
			} else if streamExitCode == 1 || streamExitCode == 2 {
				// Retry on server-side connection errors
				session.SetTestedIndexes(append(session.TestedIndexes, selectedIndex))
				utils.SafeLogf("Retrying other servers...\n")
				proxyCtxCancel()
			} else if streamExitCode == 4 {
				utils.SafeLogf("Finished handling %s request: %s\n", r.Method, r.RemoteAddr)
				return
			} else {
				// Consider client-side connection errors as complete closure
				utils.SafeLogf("Unable to write to client. Assuming stream has been closed: %s\n", r.RemoteAddr)
				return
			}
		}
	}
}
