package stream

import (
	"context"
	"fmt"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/proxy/loadbalancer"
	"m3u-stream-merger/proxy/stream/buffer"
	"m3u-stream-merger/proxy/stream/config"
	"m3u-stream-merger/store"
	"net/http"
)

type StreamInstance struct {
	Cm     *store.ConcurrencyManager
	config *config.StreamConfig
	logger logger.Logger
}

type StreamInstanceOption func(*StreamInstance)

func WithLogger(logger logger.Logger) StreamInstanceOption {
	return func(s *StreamInstance) {
		s.logger = logger
	}
}

func NewStreamInstance(
	cm *store.ConcurrencyManager,
	config *config.StreamConfig,
	opts ...StreamInstanceOption,
) (*StreamInstance, error) {
	if cm == nil {
		return nil, fmt.Errorf("concurrency manager is required")
	}

	instance := &StreamInstance{
		Cm:     cm,
		config: config,
	}

	// Apply all options
	for _, opt := range opts {
		opt(instance)
	}

	if instance.logger == nil {
		instance.logger = &logger.DefaultLogger{}
	}

	return instance, nil
}

func (instance *StreamInstance) ProxyStream(
	ctx context.Context,
	coordinator *buffer.StreamCoordinator,
	lbResult *loadbalancer.LoadBalancerResult,
	r *http.Request,
	w http.ResponseWriter,
	statusChan chan<- int,
) {
	handler := NewStreamHandler(instance.config, coordinator, instance.logger)
	result := handler.HandleStream(ctx, lbResult, w, r.RemoteAddr)

	if result.Error != nil {
		instance.logger.Logf("Stream handler status: %v", result.Error)
	}

	statusChan <- result.Status
}
