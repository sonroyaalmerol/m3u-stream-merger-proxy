package handlers

import (
	"context"
	"net/http"
	"time"

	"m3u-stream-merger/logger"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/proxy/loadbalancer"
	"m3u-stream-merger/proxy/stream"
	"m3u-stream-merger/proxy/stream/buffer"
	"m3u-stream-merger/proxy/stream/config"
	"m3u-stream-merger/store"
)

type ProxyInstance interface {
	GetConcurrencyManager() *store.ConcurrencyManager
	GetStreamRegistry() *buffer.StreamRegistry
	LoadBalancer(ctx context.Context, req *http.Request, session *store.Session) (*loadbalancer.LoadBalancerResult, error)
	ProxyStream(ctx context.Context, coordinator *buffer.StreamCoordinator,
		lbResult *loadbalancer.LoadBalancerResult, r *http.Request, w http.ResponseWriter,
		exitStatus chan<- int)
}

type DefaultProxyInstance struct {
	lbConfig     *loadbalancer.LBConfig
	streamConfig *config.StreamConfig
	registry     *buffer.StreamRegistry
	cm           *store.ConcurrencyManager
	logger       logger.Logger
}

func NewDefaultProxyInstance() *DefaultProxyInstance {
	cm := store.NewConcurrencyManager()
	streamConfig := config.NewDefaultStreamConfig()
	return &DefaultProxyInstance{
		lbConfig:     loadbalancer.NewDefaultLBConfig(),
		streamConfig: streamConfig,
		cm:           cm,
		logger:       logger.Default,
		registry:     buffer.NewStreamRegistry(streamConfig, cm, logger.Default, 30*time.Second),
	}
}

func (sm *DefaultProxyInstance) LoadBalancer(ctx context.Context, req *http.Request,
	session *store.Session) (*loadbalancer.LoadBalancerResult, error) {
	instance := loadbalancer.NewLoadBalancerInstance(sm.cm, sm.lbConfig,
		loadbalancer.WithLogger(sm.logger))
	return instance.Balance(ctx, req, session)
}

func (sm *DefaultProxyInstance) ProxyStream(ctx context.Context, coordinator *buffer.StreamCoordinator,
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

func (sm *DefaultProxyInstance) GetConcurrencyManager() *store.ConcurrencyManager {
	return sm.cm
}

func (sm *DefaultProxyInstance) GetStreamRegistry() *buffer.StreamRegistry {
	return sm.registry
}
