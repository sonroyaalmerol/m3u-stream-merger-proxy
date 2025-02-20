package loadbalancer

import (
	"context"
	"fmt"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/sourceproc"
	"m3u-stream-merger/store"
	"m3u-stream-merger/utils"
	"net/http"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

type LoadBalancerInstance struct {
	Info            *sourceproc.StreamInfo
	Cm              *store.ConcurrencyManager
	config          *LBConfig
	httpClient      HTTPClient
	logger          logger.Logger
	indexProvider   IndexProvider
	slugParser      SlugParser
	testedIndexes   map[string][]string
	testedIndexesMu sync.RWMutex
}

type LoadBalancerInstanceOption func(*LoadBalancerInstance)

func WithHTTPClient(client HTTPClient) LoadBalancerInstanceOption {
	return func(s *LoadBalancerInstance) {
		s.httpClient = client
	}
}

func WithLogger(logger logger.Logger) LoadBalancerInstanceOption {
	return func(s *LoadBalancerInstance) {
		s.logger = logger
	}
}

func WithIndexProvider(provider IndexProvider) LoadBalancerInstanceOption {
	return func(s *LoadBalancerInstance) {
		s.indexProvider = provider
	}
}

func WithSlugParser(parser SlugParser) LoadBalancerInstanceOption {
	return func(s *LoadBalancerInstance) {
		s.slugParser = parser
	}
}

func NewLoadBalancerInstance(
	cm *store.ConcurrencyManager,
	cfg *LBConfig,
	opts ...LoadBalancerInstanceOption,
) *LoadBalancerInstance {
	instance := &LoadBalancerInstance{
		Cm:            cm,
		config:        cfg,
		httpClient:    utils.HTTPClient,
		logger:        &logger.DefaultLogger{},
		indexProvider: &DefaultIndexProvider{},
		slugParser:    &DefaultSlugParser{},
		testedIndexes: make(map[string][]string),
	}

	for _, opt := range opts {
		opt(instance)
	}

	return instance
}

type LoadBalancerResult struct {
	Response *http.Response
	URL      string
	Index    string
	SubIndex string
}

func (instance *LoadBalancerInstance) GetStreamId(req *http.Request) string {
	streamId := strings.Split(path.Base(req.URL.Path), ".")[0]
	if streamId == "" {
		return ""
	}
	streamId = strings.TrimPrefix(streamId, "/")

	return streamId
}

func (instance *LoadBalancerInstance) Balance(ctx context.Context, req *http.Request) (*LoadBalancerResult, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context cannot be nil")
	}
	if req == nil {
		return nil, fmt.Errorf("req cannot be nil")
	}
	if req.Method == "" {
		return nil, fmt.Errorf("req.Method cannot be empty")
	}
	if req.URL == nil {
		return nil, fmt.Errorf("req.URL cannot be empty")
	}

	streamId := instance.GetStreamId(req)

	err := instance.fetchBackendUrls(streamId)
	if err != nil {
		return nil, fmt.Errorf("error fetching sources for: %s", streamId)
	}

	backoff := proxy.NewBackoffStrategy(time.Duration(instance.config.RetryWait)*time.Second, 0)

	for lap := 0; lap < instance.config.MaxRetries || instance.config.MaxRetries == 0; lap++ {
		instance.logger.Debugf("Stream attempt %d out of %d", lap+1, instance.config.MaxRetries)

		result, err := instance.tryAllStreams(ctx, req.Method, streamId)
		if err == nil {
			return result, nil
		}
		instance.logger.Debugf("tryAllStreams error: %v", err)

		if err == context.Canceled {
			return nil, fmt.Errorf("cancelling load balancer")
		}

		instance.clearTested(streamId)

		select {
		case <-time.After(backoff.Next()):
		case <-ctx.Done():
			return nil, fmt.Errorf("cancelling load balancer")
		}
	}

	return nil, fmt.Errorf("error fetching stream: exhausted all streams")
}

func (instance *LoadBalancerInstance) GetNumTestedIndexes(streamId string) int {
	return len(instance.testedIndexes[streamId])
}

func (instance *LoadBalancerInstance) fetchBackendUrls(streamUrl string) error {
	stream, err := instance.slugParser.GetStreamBySlug(streamUrl)
	if err != nil {
		return err
	}

	instance.logger.Debugf("Decoded slug: %v", stream)

	// Validate URLs map
	if len(stream.URLs) == 0 {
		return fmt.Errorf("stream has no URLs configured")
	}

	// Validate that at least one index has URLs
	hasValidUrls := false
	for _, innerMap := range stream.URLs {
		if len(innerMap) > 0 {
			hasValidUrls = true
			break
		}
	}
	if !hasValidUrls {
		return fmt.Errorf("stream has no valid URLs")
	}

	instance.Info = stream

	return nil
}

func (instance *LoadBalancerInstance) tryAllStreams(ctx context.Context, method string, streamId string) (*LoadBalancerResult, error) {
	instance.logger.Logf("Trying all stream urls for: %s", streamId)
	if instance.indexProvider == nil {
		return nil, fmt.Errorf("index provider cannot be nil")
	}
	m3uIndexes := instance.indexProvider.GetM3UIndexes()
	if len(m3uIndexes) == 0 {
		return nil, fmt.Errorf("no M3U indexes available")
	}

	select {
	case <-ctx.Done():
		return nil, context.Canceled
	default:
		done := make(map[string]bool)
		initialCount := len(m3uIndexes)

		for len(done) < initialCount {
			sort.Slice(m3uIndexes, func(i, j int) bool {
				return instance.Cm.ConcurrencyPriorityValue(m3uIndexes[i]) > instance.Cm.ConcurrencyPriorityValue(m3uIndexes[j])
			})

			var index string
			for _, idx := range m3uIndexes {
				if !done[idx] {
					index = idx
					break
				}
			}

			done[index] = true

			innerMap, ok := instance.Info.URLs[index]
			if !ok {
				instance.logger.Errorf("Channel not found from M3U_%s: %s", index, instance.Info.Title)
				continue
			}

			result, err := instance.tryStreamUrls(method, streamId, index, innerMap)
			if err == nil {
				return result, nil
			}

			select {
			case <-ctx.Done():
				return nil, context.Canceled
			default:
				continue
			}
		}
	}
	return nil, fmt.Errorf("no available streams")
}

func (instance *LoadBalancerInstance) tryStreamUrls(
	method string,
	streamId string,
	index string,
	urls map[string]string,
) (*LoadBalancerResult, error) {
	if instance.httpClient == nil {
		return nil, fmt.Errorf("HTTP client cannot be nil")
	}

	sortedSubIndexes := sourceprocSortStreamSubUrls(urls)

	var wg sync.WaitGroup
	resultCh := make(chan *streamTestResult, len(sortedSubIndexes))

	for _, subIndex := range sortedSubIndexes {
		fileContent, ok := urls[subIndex]
		if !ok {
			continue
		}

		url := fileContent
		fileContentSplit := strings.SplitN(fileContent, ":::", 2)
		if len(fileContentSplit) == 2 {
			url = fileContentSplit[1]
		}

		candidateId := index + "|" + subIndex

		instance.testedIndexesMu.RLock()
		alreadyTested := contains(instance.testedIndexes[streamId], candidateId)
		instance.testedIndexesMu.RUnlock()
		if alreadyTested {
			instance.logger.Debugf(
				"Skipping M3U_%s|%s: already tested", index, subIndex,
			)
			continue
		}

		if instance.Cm.CheckConcurrency(index) {
			instance.logger.Debugf("Concurrency limit reached for M3U_%s: %s", index, url)
			continue
		}

		wg.Add(1)
		go func(subIndex, url, candidateId string) {
			defer wg.Done()

			req, err := http.NewRequest(method, url, nil)
			if err != nil {
				instance.logger.Errorf("Error creating request: %s", err.Error())
				instance.markTested(streamId, candidateId)
				resultCh <- &streamTestResult{err: err}
				return
			}

			// Do the HTTP request.
			resp, err := instance.httpClient.Do(req)
			if err != nil {
				instance.logger.Errorf("Error fetching stream: %s", err.Error())
				instance.markTested(streamId, candidateId)
				resultCh <- &streamTestResult{err: err}
				return
			}
			if resp == nil {
				instance.logger.Errorf("Received nil response from HTTP client")
				instance.markTested(streamId, candidateId)
				resultCh <- &streamTestResult{err: fmt.Errorf("nil response")}
				return
			}
			if resp.StatusCode != http.StatusOK {
				instance.logger.Errorf("Non-200 status %d for %s %s",
					resp.StatusCode, method, url)
				instance.markTested(streamId, candidateId)
				resultCh <- &streamTestResult{
					err: fmt.Errorf("non-200 status: %d", resp.StatusCode),
				}
				return
			}

			health, evalErr := evaluateBufferHealth(resp, instance.config.BufferChunk)
			if evalErr != nil {
				instance.logger.Errorf("Error evaluating buffer health: %s", evalErr.Error())
				instance.markTested(streamId, candidateId)
				resultCh <- &streamTestResult{err: evalErr}
				return
			}

			instance.logger.Debugf("Successful stream from %s (health: %f)",
				url, health)
			resultCh <- &streamTestResult{
				result: &LoadBalancerResult{
					Response: resp,
					URL:      url,
					Index:    index,
					SubIndex: subIndex,
				},
				health: health,
				err:    nil,
			}
		}(subIndex, url, candidateId)
	}

	wg.Wait()
	close(resultCh)

	var bestResult *streamTestResult
	for res := range resultCh {
		if res.err != nil {
			continue
		}
		if bestResult == nil || res.health > bestResult.health {
			bestResult = res
		}
	}

	if bestResult != nil {
		return bestResult.result, nil
	}
	return nil, fmt.Errorf("all urls failed")
}

func (instance *LoadBalancerInstance) markTested(streamId string, id string) {
	instance.testedIndexesMu.Lock()
	instance.testedIndexes[streamId] = append(instance.testedIndexes[streamId], id)
	instance.testedIndexesMu.Unlock()
}

func (instance *LoadBalancerInstance) clearTested(streamId string) {
	instance.testedIndexesMu.Lock()
	instance.testedIndexes[streamId] = []string{}
	instance.testedIndexesMu.Unlock()
}
