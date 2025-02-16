package stream

import (
	"context"
	"fmt"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/proxy/loadbalancer"
	"m3u-stream-merger/proxy/stream/buffer"
	"m3u-stream-merger/proxy/stream/config"
	"m3u-stream-merger/proxy/stream/failovers"
	"m3u-stream-merger/store"
	"m3u-stream-merger/utils"
	"net/http"
)

type StreamInstance struct {
	Cm           *store.ConcurrencyManager
	config       *config.StreamConfig
	logger       logger.Logger
	failoverProc *failovers.M3U8Processor
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
		Cm:           cm,
		config:       config,
		failoverProc: failovers.NewM3U8Processor(&logger.DefaultLogger{}),
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

	if lbResult.IsInvalid.Load() && utils.IsAnM3U8Media(lbResult.Response) {
		instance.logger.Logf("Source is known to have an incompatible media type for an M3U8. Trying a fallback passthrough method.")
		instance.logger.Logf("Passthrough method will not have any shared buffer or concurrency check support.")

		if err := instance.failoverProc.ProcessM3U8Stream(lbResult, w); err != nil {
			instance.logger.Logf("Stream is invalid. Retrying other servers...")
			statusChan <- proxy.StatusIncompatible
			return
		}

		statusChan <- proxy.StatusM3U8Parsed
		return
	}

	if utils.IsAnM3U8Media(lbResult.Response) {
		lbResult.Response.Body.Close()
	}

	statusChan <- result.Status
}
