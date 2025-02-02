package handlers

import (
	"context"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/proxy/loadbalancer"
	"m3u-stream-merger/proxy/stream"
	"m3u-stream-merger/store"
	"net/http"
)

type DefaultStreamManager struct {
	StreamManager
	lbConfig     *loadbalancer.LBConfig
	streamConfig *stream.StreamConfig
	cm           *store.ConcurrencyManager
	logger       logger.Logger
}

func NewDefaultStreamManager() *DefaultStreamManager {
	sm := &DefaultStreamManager{}

	sm.lbConfig = loadbalancer.NewDefaultLBConfig()
	sm.streamConfig = stream.NewDefaultStreamConfig()
	sm.cm = store.NewConcurrencyManager()
	sm.logger = logger.Default

	return sm
}

func (sm *DefaultStreamManager) LoadBalancer(ctx context.Context, req *http.Request, session *store.Session) (*http.Response, string, string, string, error) {
	instance := loadbalancer.NewLoadBalancerInstance(sm.cm, sm.lbConfig, loadbalancer.WithLogger(sm.logger))
	result, err := instance.Balance(ctx, req, session)
	if err != nil {
		return nil, "", "", "", err
	}

	return result.Response, result.URL, result.Index, result.SubIndex, nil
}

func (sm *DefaultStreamManager) ProxyStream(ctx context.Context, resp *http.Response, r *http.Request, w http.ResponseWriter, exitStatus chan<- int) {
	instance, err := stream.NewStreamInstance(
		sm.cm,
		sm.streamConfig,
		stream.WithLogger(sm.logger),
	)
	if err != nil {
		sm.logger.Errorf("Failed to create stream instance: %v", err)
		exitStatus <- proxy.StatusServerError
		return
	}

	instance.ProxyStream(ctx, resp, r, w, exitStatus)
}

func (sm *DefaultStreamManager) GetConcurrencyManager() *store.ConcurrencyManager {
	return sm.cm
}
