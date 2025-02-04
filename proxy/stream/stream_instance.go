package stream

import (
	"bufio"
	"context"
	"fmt"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/proxy/loadbalancer"
	"m3u-stream-merger/store"
	"m3u-stream-merger/utils"
	"net/http"
	"net/url"
)

type StreamInstance struct {
	Cm     *store.ConcurrencyManager
	config *StreamConfig
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
	config *StreamConfig,
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
	coordinator *StreamCoordinator,
	lbResult *loadbalancer.LoadBalancerResult,
	r *http.Request,
	w http.ResponseWriter,
	statusChan chan<- int,
) {
	if r.Method != http.MethodGet || utils.IsAnM3U8Media(lbResult.Response) {
		instance.handleM3U8Stream(lbResult.Response, w, statusChan)
		return
	}

	instance.handleMediaStream(ctx, coordinator, lbResult, r, w, statusChan)
}

func (instance *StreamInstance) handleM3U8Stream(
	resp *http.Response,
	w http.ResponseWriter,
	statusChan chan<- int,
) {
	scanner := bufio.NewScanner(resp.Body)
	base, err := url.Parse(resp.Request.URL.String())
	if err != nil {
		instance.logger.Errorf("Invalid base URL for M3U8 stream: %v", err)
		statusChan <- proxy.StatusM3U8ParseError
		return
	}

	processor := NewM3U8Processor(instance.logger)
	if err := processor.ProcessM3U8Stream(scanner, w, base); err != nil {
		instance.logger.Errorf("Failed to process M3U8 stream: %v", err)
		statusChan <- proxy.StatusM3U8ParseError
		return
	}

	statusChan <- proxy.StatusM3U8Parsed
}

func (instance *StreamInstance) handleMediaStream(
	ctx context.Context,
	coordinator *StreamCoordinator,
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
