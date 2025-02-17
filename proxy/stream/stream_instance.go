package stream

import (
	"context"
	"fmt"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/proxy/client"
	"m3u-stream-merger/proxy/loadbalancer"
	"m3u-stream-merger/proxy/stream/buffer"
	"m3u-stream-merger/proxy/stream/config"
	"m3u-stream-merger/proxy/stream/failovers"
	"m3u-stream-merger/store"
	"m3u-stream-merger/utils"
	"strings"
	"sync"
)

type StreamInstance struct {
	Cm           *store.ConcurrencyManager
	config       *config.StreamConfig
	logger       logger.Logger
	failoverProc *failovers.M3U8Processor
	warned       sync.Map // map[string]struct{}{}
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
	streamClient *client.StreamClient,
	statusChan chan<- int,
) {
	handler := NewStreamHandler(instance.config, coordinator, instance.logger)

	var result StreamResult
	if lbResult.Response.StatusCode == 206 || strings.HasSuffix(lbResult.URL, ".mp4") {
		handler.logger.Logf("VOD request detected from: %s", streamClient.Request.RemoteAddr)
		handler.logger.Warn("VODs do not support shared buffer.")
		result = handler.HandleDirectStream(ctx, lbResult, streamClient)
	} else {
		if _, ok := instance.warned.Load(lbResult.URL); !ok {
			result = handler.HandleStream(ctx, lbResult, streamClient)
		} else {
			result = StreamResult{
				Status: proxy.StatusIncompatible,
			}
		}
	}
	if result.Error != nil {
		if result.Status != proxy.StatusIncompatible {
			instance.logger.Errorf("Stream handler status: %v", result.Error)
		}
	}

	if result.Status == proxy.StatusIncompatible && utils.IsAnM3U8Media(lbResult.Response) {
		if _, ok := instance.warned.Load(lbResult.URL); !ok {
			instance.logger.Logf("Source is known to have an incompatible media type for an M3U8. Trying a fallback passthrough method.")
			instance.logger.Logf("Passthrough method will not have any shared buffer. Concurrency support might be unreliable.")
			instance.warned.Store(lbResult.URL, struct{}{})
		}

		if err := instance.failoverProc.ProcessM3U8Stream(lbResult, streamClient); err != nil {
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
