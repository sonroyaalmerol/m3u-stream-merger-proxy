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
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/puzpuzpuz/xsync/v3"
)

type LoadBalancerInstance struct {
	infoMu        sync.Mutex
	info          *sourceproc.StreamInfo
	Cm            *store.ConcurrencyManager
	config        *LBConfig
	httpClient    HTTPClient
	logger        logger.Logger
	indexProvider IndexProvider
	slugParser    SlugParser
	testedIndexes *xsync.MapOf[string, []string]
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
		testedIndexes: xsync.NewMapOf[string, []string](),
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

func (instance *LoadBalancerInstance) GetStreamInfo() *sourceproc.StreamInfo {
	instance.infoMu.Lock()
	defer instance.infoMu.Unlock()
	return instance.info
}

func (instance *LoadBalancerInstance) SetStreamInfo(info *sourceproc.StreamInfo) {
	instance.infoMu.Lock()
	defer instance.infoMu.Unlock()
	instance.info = info
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
	streamTested, ok := instance.testedIndexes.Load(streamId)
	if !ok {
		return 0
	}
	return len(streamTested)
}

func (instance *LoadBalancerInstance) fetchBackendUrls(streamUrl string) error {
	stream, err := instance.slugParser.GetStreamBySlug(streamUrl)
	if err != nil {
		return err
	}

	instance.logger.Debugf("Decoded slug: %v", stream)

	if stream.URLs == nil {
		stream.URLs = xsync.NewMapOf[string, map[string]string]()
	}
	// Validate URLs map
	if stream.URLs.Size() == 0 {
		return fmt.Errorf("stream has no URLs configured")
	}

	// Validate that at least one index has URLs
	hasValidUrls := false
	stream.URLs.Range(func(_ string, innerMap map[string]string) bool {
		if len(innerMap) > 0 {
			hasValidUrls = true
			return false
		}

		return true
	})
	if !hasValidUrls {
		return fmt.Errorf("stream has no valid URLs")
	}

	instance.SetStreamInfo(stream)

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

			innerMap, ok := instance.GetStreamInfo().URLs.Load(index)
			if !ok {
				instance.logger.Errorf("Channel not found from M3U_%s: %s", index, instance.GetStreamInfo().Title)
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

	for _, subIndex := range sourceproc.SortStreamSubUrls(urls) {
		fileContent, ok := urls[subIndex]
		if !ok {
			continue
		}

		url := fileContent
		fileContentSplit := strings.SplitN(fileContent, ":::", 2)
		if len(fileContentSplit) == 2 {
			url = fileContentSplit[1]
		}

		id := index + "|" + subIndex
		var alreadyTested bool
		streamTested, ok := instance.testedIndexes.Load(streamId)
		if ok {
			alreadyTested = slices.Contains(streamTested, index+"|"+subIndex)
		}

		if alreadyTested {
			instance.logger.Debugf("Skipping M3U_%s|%s: marked as previous stream", index, subIndex)
			continue
		}

		if instance.Cm.CheckConcurrency(index) {
			instance.logger.Debugf("Concurrency limit reached for M3U_%s: %s", index, url)
			continue
		}

		req, err := http.NewRequest(method, url, nil)
		if err != nil {
			instance.logger.Errorf("Error creating request: %s", err.Error())
			instance.markTested(streamId, id)
			continue
		}

		resp, err := instance.httpClient.Do(req)
		if err != nil {
			instance.logger.Errorf("Error fetching stream: %s", err.Error())
			instance.markTested(streamId, id)
			continue
		}

		if resp == nil {
			instance.logger.Errorf("Received nil response from HTTP client")
			instance.markTested(streamId, id)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			instance.logger.Errorf("Non-200 status code received: %d for %s %s", resp.StatusCode, method, url)
			instance.markTested(streamId, id)
			continue
		}

		instance.logger.Debugf("Successfully fetched stream from %s with method %s", url, method)

		return &LoadBalancerResult{
			Response: resp,
			URL:      url,
			Index:    index,
			SubIndex: subIndex,
		}, nil
	}

	return nil, fmt.Errorf("all urls failed")
}

func (instance *LoadBalancerInstance) markTested(streamId string, id string) {
	instance.testedIndexes.Compute(streamId, func(val []string, _ bool) (newValue []string, delete bool) {
		val = append(val, id)
		return val, false
	})
}

func (instance *LoadBalancerInstance) clearTested(streamId string) {
	instance.testedIndexes.Delete(streamId)
}
