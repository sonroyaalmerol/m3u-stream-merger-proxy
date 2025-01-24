package handlers

import (
	"context"
	"encoding/base64"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/utils"
	"net/http"
	"os"
	"path"
	"strings"
)

func M3UVideoHandler(w http.ResponseWriter, r *http.Request) {
	debug := os.Getenv("DEBUG") == "true"

	ctx := r.Context()

	utils.SafeLogf("Received request from %s for URL: %s\n", r.RemoteAddr, r.URL.Path)

	streamUrl := strings.Split(path.Base(r.URL.Path), ".")[0]
	if streamUrl == "" {
		utils.SafeLogf("Invalid stream for request from %s: %s\n", r.RemoteAddr, r.URL.Path)
		http.NotFound(w, r)
		return
	}

	decodedStreamUrl, err := base64.URLEncoding.DecodeString(streamUrl)
	if err != nil {
		utils.SafeLogf("Error retrieving stream url %s: %v\n", streamUrl, err)
		http.NotFound(w, r)
		return
	}

	resp, err := utils.CustomHttpRequest(r.Method, string(decodedStreamUrl))
	if err != nil {
		utils.SafeLogf("Error fetching stream: %s\n", err.Error())
	}
	defer resp.Body.Close()

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

	utils.SafeLogf("Proxying %s to %s\n", r.RemoteAddr, decodedStreamUrl)
	proxyCtx, proxyCtxCancel := context.WithCancel(ctx)
	defer proxyCtxCancel()

	streamExitCode := proxy.ProxyVideoStream(proxyCtx, resp, r, w)
	utils.SafeLogf("Exit code %d received from %s\n", streamExitCode, decodedStreamUrl)

	if streamExitCode == 2 && utils.EOFIsExpected(resp) {
		utils.SafeLogf("Successfully proxied playlist: %s\n", r.RemoteAddr)
	} else if streamExitCode == 1 || streamExitCode == 2 {
		// Retry on server-side connection errors
		utils.SafeLogf("Server failed to respond: %s\n", decodedStreamUrl)
	} else if streamExitCode == 4 {
		utils.SafeLogf("Finished handling %s request: %s\n", r.Method, r.RemoteAddr)
	} else {
		// Consider client-side connection errors as complete closure
		utils.SafeLogf("Unable to write to client. Assuming stream has been closed: %s\n", r.RemoteAddr)
	}
}
