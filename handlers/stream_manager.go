package handlers

import (
	"context"
	"net/http"
	"time"

	"m3u-stream-merger/logger"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/proxy/loadbalancer"
	"m3u-stream-merger/proxy/stream"
	"m3u-stream-merger/store"
)

type StreamManager interface {
	GetConcurrencyManager() *store.ConcurrencyManager
	GetStreamRegistry() *stream.StreamRegistry
	LoadBalancer(ctx context.Context, req *http.Request, session *store.Session) (*loadbalancer.LoadBalancerResult, error)
	ProxyStream(ctx context.Context, coordinator *stream.StreamCoordinator,
		lbResult *loadbalancer.LoadBalancerResult, r *http.Request, w http.ResponseWriter,
		exitStatus chan<- int)
}

type DefaultStreamManager struct {
	lbConfig     *loadbalancer.LBConfig
	streamConfig *stream.StreamConfig
	registry     *stream.StreamRegistry
	cm           *store.ConcurrencyManager
	logger       logger.Logger
}

func NewDefaultStreamManager() *DefaultStreamManager {
	cm := store.NewConcurrencyManager()
	streamConfig := stream.NewDefaultStreamConfig()
	return &DefaultStreamManager{
		lbConfig:     loadbalancer.NewDefaultLBConfig(),
		streamConfig: streamConfig,
		cm:           cm,
		logger:       logger.Default,
		registry:     stream.NewStreamRegistry(streamConfig, cm, logger.Default, 30*time.Second),
	}
}

func (sm *DefaultStreamManager) LoadBalancer(ctx context.Context, req *http.Request,
	session *store.Session) (*loadbalancer.LoadBalancerResult, error) {
	instance := loadbalancer.NewLoadBalancerInstance(sm.cm, sm.lbConfig,
		loadbalancer.WithLogger(sm.logger))
	return instance.Balance(ctx, req, session)
}

func (sm *DefaultStreamManager) ProxyStream(ctx context.Context, coordinator *stream.StreamCoordinator,
	lbResult *loadbalancer.LoadBalancerResult, r *http.Request, w http.ResponseWriter,
	exitStatus chan<- int) {
	instance, err := stream.NewStreamInstance(sm.cm, sm.streamConfig,
		stream.WithLogger(sm.logger))
	if err != nil {
		sm.logger.Errorf("Failed to create stream instance: %v", err)
		exitStatus <- proxy.StatusServerError
		return
	}
	instance.ProxyStream(ctx, coordinator, lbResult, r, w, exitStatus)
}

func (sm *DefaultStreamManager) GetConcurrencyManager() *store.ConcurrencyManager {
	return sm.cm
}

func (sm *DefaultStreamManager) GetStreamRegistry() *stream.StreamRegistry {
	return sm.registry
}
