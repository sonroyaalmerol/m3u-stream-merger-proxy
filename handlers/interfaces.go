package handlers

import (
	"context"
	"m3u-stream-merger/proxy/stream"
	"m3u-stream-merger/store"
	"net/http"
)

type StreamManager interface {
	GetConcurrencyManager() *store.ConcurrencyManager
	GetStreamRegistry() *stream.StreamRegistry
	LoadBalancer(ctx context.Context, request *http.Request, session *store.Session) (*http.Response, string, string, string, error)
	ProxyStream(ctx context.Context, coordinator *stream.StreamCoordinator, m3uIndex string, resp *http.Response, r *http.Request, w http.ResponseWriter, exitStatus chan<- int)
}

