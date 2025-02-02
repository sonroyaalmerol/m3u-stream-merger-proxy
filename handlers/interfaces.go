package handlers

import (
	"context"
	"m3u-stream-merger/proxy/loadbalancer"
	"m3u-stream-merger/proxy/stream"
	"m3u-stream-merger/store"
	"net/http"
)

type StreamManager interface {
	GetConcurrencyManager() *store.ConcurrencyManager
	GetStreamRegistry() *stream.StreamRegistry
	LoadBalancer(ctx context.Context, request *http.Request, session *store.Session) (*loadbalancer.LoadBalancerResult, error)
	ProxyStream(ctx context.Context, coordinator *stream.StreamCoordinator, lbResult *loadbalancer.LoadBalancerResult, r *http.Request, w http.ResponseWriter, exitStatus chan<- int)
}

