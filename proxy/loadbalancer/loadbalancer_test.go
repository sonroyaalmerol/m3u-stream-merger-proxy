package loadbalancer

import (
	"context"
	"errors"
	"fmt"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/sourceproc"
	"m3u-stream-merger/store"
	"m3u-stream-merger/utils/safemap"
	"net/http"
	"os"
	"sort"
	"sync"
	"testing"
	"time"
)

// Helper function to create test requests
func newTestRequest(method string) *http.Request {
	req, _ := http.NewRequest(method, "/test-stream.m3u8", nil)
	return req
}

// Mock implementations
type mockSlugParser struct {
	streams map[string]*sourceproc.StreamInfo
}

func (m *mockSlugParser) GetStreamBySlug(slug string) (*sourceproc.StreamInfo, error) {
	if info, ok := m.streams[slug]; ok {
		return info, nil
	}
	return &sourceproc.StreamInfo{}, errors.New("stream not found")
}

type mockHTTPClient struct {
	responses map[string]*http.Response
	errors    map[string]error
	delay     time.Duration
	mu        sync.RWMutex
}

func (m *mockHTTPClient) Do(req *http.Request) (*http.Response, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	fmt.Printf("Mock client received request: %s %s\n", req.Method, req.URL.String())

	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}

	if err := m.errors[req.URL.String()]; err != nil {
		return nil, err
	}

	resp := m.responses[req.URL.String()]
	if resp == nil {
		return &http.Response{
			StatusCode: http.StatusNotFound,
		}, nil
	}

	return &http.Response{
		StatusCode: resp.StatusCode,
	}, nil
}

type mockIndexProvider struct {
	indexes []string
}

func (m *mockIndexProvider) GetM3UIndexes() []string {
	return m.indexes
}

// Test setup helper
func setupTestInstance(t *testing.T) (*LoadBalancerInstance, *mockHTTPClient, *mockIndexProvider) {
	client := &mockHTTPClient{
		responses: make(map[string]*http.Response),
		errors:    make(map[string]error),
	}

	client.responses["http://test1.com/stream"] = &http.Response{
		StatusCode: http.StatusOK,
	}
	client.responses["http://test1.com/backup"] = &http.Response{
		StatusCode: http.StatusOK,
	}
	client.responses["http://test2.com/stream"] = &http.Response{
		StatusCode: http.StatusOK,
	}

	indexProvider := &mockIndexProvider{
		indexes: []string{"1", "2"},
	}

	urls := safemap.New[string, map[string]string]()
	urls.Set("1", map[string]string{
		"a": "http://test1.com/stream",
		"b": "http://test1.com/backup",
	})
	urls.Set("2", map[string]string{
		"a": "http://test2.com/stream",
	})

	slugParser := &mockSlugParser{
		streams: map[string]*sourceproc.StreamInfo{
			"test-stream": {
				Title: "Test Stream",
				URLs:  urls,
			},
		},
	}

	cm := store.NewConcurrencyManager()
	cfg := NewDefaultLBConfig()

	instance := NewLoadBalancerInstance(
		cm,
		cfg,
		WithHTTPClient(client),
		WithLogger(logger.Default),
		WithIndexProvider(indexProvider),
		WithSlugParser(slugParser),
	)

	err := instance.fetchBackendUrls("test-stream")
	if err != nil {
		t.Fatalf("Failed to create LoadBalancerInstance: %v", err)
	}

	return instance, client, indexProvider
}

func TestNewLoadBalancerInstance(t *testing.T) {
	urls := safemap.New[string, map[string]string]()
	urls.Set("1", map[string]string{
		"a": "http://test1.com/stream",
	})

	slugParser := &mockSlugParser{
		streams: map[string]*sourceproc.StreamInfo{
			"test-stream": {
				Title: "Test Stream",
				URLs:  urls,
			},
		},
	}

	tests := []struct {
		name        string
		streamURL   string
		wantErr     bool
		errContains string
	}{
		{
			name:      "valid stream",
			streamURL: "test-stream",
			wantErr:   false,
		},
		{
			name:        "invalid stream",
			streamURL:   "nonexistent",
			wantErr:     true,
			errContains: "stream not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cm := store.NewConcurrencyManager()
			cfg := NewDefaultLBConfig()

			i := NewLoadBalancerInstance(cm, cfg, WithSlugParser(slugParser))
			err := i.fetchBackendUrls(tt.streamURL)

			if (err != nil) != tt.wantErr {
				t.Errorf("NewLoadBalancerInstance() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && err != nil && tt.errContains != "" {
				if err.Error() != tt.errContains {
					t.Errorf("NewLoadBalancerInstance() error = %v, want error %v", err, tt.errContains)
				}
			}
		})
	}
}

func TestLoadBalancer(t *testing.T) {
	tests := []struct {
		name           string
		setupMocks     func(*mockHTTPClient, *mockIndexProvider)
		expectErr      bool
		expectedURL    string
		expectedIndex  string
		expectedSubIdx string
	}{
		{
			name: "successful first attempt",
			setupMocks: func(client *mockHTTPClient, provider *mockIndexProvider) {
				client.responses = make(map[string]*http.Response)
				client.errors = make(map[string]error)
				client.responses["http://test1.com/stream"] = &http.Response{
					StatusCode: 200,
				}
			},
			expectErr:      false,
			expectedURL:    "http://test1.com/stream",
			expectedIndex:  "1",
			expectedSubIdx: "a",
		},
		{
			name: "fallback to backup stream",
			setupMocks: func(client *mockHTTPClient, provider *mockIndexProvider) {
				client.responses = make(map[string]*http.Response)
				client.errors = make(map[string]error)
				client.errors["http://test1.com/stream"] = errors.New("connection failed")
				client.responses["http://test1.com/backup"] = &http.Response{
					StatusCode: 200,
				}
			},
			expectErr:      false,
			expectedURL:    "http://test1.com/backup",
			expectedIndex:  "1",
			expectedSubIdx: "b",
		},
		{
			name: "all attempts fail",
			setupMocks: func(client *mockHTTPClient, provider *mockIndexProvider) {
				client.responses = make(map[string]*http.Response)
				client.errors = make(map[string]error)
				client.errors["http://test1.com/stream"] = errors.New("connection failed")
				client.errors["http://test1.com/backup"] = errors.New("connection failed")
				client.errors["http://test2.com/stream"] = errors.New("connection failed")
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			instance, client, provider := setupTestInstance(t)
			tt.setupMocks(client, provider)

			ctx := context.Background()
			result, err := instance.Balance(ctx, newTestRequest(http.MethodGet))

			if tt.expectErr {
				if err == nil {
					t.Error("LoadBalancer() expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("LoadBalancer() unexpected error: %v", err)
				return
			}

			if result.URL != tt.expectedURL {
				t.Errorf("LoadBalancer() got URL = %v, want %v", result.URL, tt.expectedURL)
			}

			if result.Index != tt.expectedIndex {
				t.Errorf("LoadBalancer() got Index = %v, want %v", result.Index, tt.expectedIndex)
			}

			if result.SubIndex != tt.expectedSubIdx {
				t.Errorf("LoadBalancer() got SubIndex = %v, want %v", result.SubIndex, tt.expectedSubIdx)
			}
		})
	}
}

func TestLoadBalancerWithHTTPStatusCodes(t *testing.T) {
	tests := []struct {
		name        string
		setupMocks  func(*mockHTTPClient)
		expectErr   bool
		expectedURL string
	}{
		{
			name: "handles 301 redirect",
			setupMocks: func(client *mockHTTPClient) {
				client.responses = map[string]*http.Response{
					"http://test1.com/stream": {StatusCode: 301},
					"http://test1.com/backup": {StatusCode: 200},
				}
			},
			expectErr:   false,
			expectedURL: "http://test1.com/backup",
		},
		{
			name: "handles 404 not found",
			setupMocks: func(client *mockHTTPClient) {
				client.responses = map[string]*http.Response{
					"http://test1.com/stream": {StatusCode: 404},
					"http://test1.com/backup": {StatusCode: 200},
				}
			},
			expectErr:   false,
			expectedURL: "http://test1.com/backup",
		},
		{
			name: "all endpoints return 500",
			setupMocks: func(client *mockHTTPClient) {
				client.responses = map[string]*http.Response{
					"http://test1.com/stream": {StatusCode: 500},
					"http://test1.com/backup": {StatusCode: 500},
					"http://test2.com/stream": {StatusCode: 500},
				}
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			instance, client, _ := setupTestInstance(t)
			tt.setupMocks(client)

			ctx := context.Background()

			result, err := instance.Balance(ctx, newTestRequest(http.MethodGet))

			if tt.expectErr {
				if err == nil {
					t.Error("LoadBalancer() expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("LoadBalancer() unexpected error: %v", err)
				return
			}

			if result.URL != tt.expectedURL {
				t.Errorf("LoadBalancer() got URL = %v, want %v", result.URL, tt.expectedURL)
			}
		})
	}
}

func TestLoadBalancerWithDifferentHTTPMethods(t *testing.T) {
	methods := []string{
		http.MethodGet,
		http.MethodHead,
		http.MethodPost,
		http.MethodPut,
		http.MethodDelete,
		http.MethodOptions,
	}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			instance, client, _ := setupTestInstance(t)

			client.mu.Lock()
			client.responses = make(map[string]*http.Response)
			client.errors = make(map[string]error)
			client.responses["http://test1.com/stream"] = &http.Response{
				StatusCode: http.StatusOK,
			}
			client.mu.Unlock()

			ctx := context.Background()

			result, err := instance.Balance(ctx, newTestRequest(method))
			if err != nil {
				t.Errorf("LoadBalancer() unexpected error with method %s: %v", method, err)
				return
			}

			if result.URL != "http://test1.com/stream" {
				t.Errorf("LoadBalancer() unexpected URL with method %s: got %v", method, result.URL)
			}
		})
	}
}

func TestConcurrentAccess(t *testing.T) {
	instance, client, _ := setupTestInstance(t)

	client.mu.Lock()
	client.responses = make(map[string]*http.Response)
	client.errors = make(map[string]error)
	client.responses["http://test1.com/stream"] = &http.Response{
		StatusCode: http.StatusOK,
	}
	client.mu.Unlock()

	const numGoroutines = 10
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	results := make(chan string, numGoroutines)
	errors := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()

			ctx := context.Background()
			result, err := instance.Balance(ctx, newTestRequest(http.MethodGet))
			if err != nil {
				errors <- fmt.Errorf("goroutine %d error: %v", id, err)
				return
			}
			results <- result.URL
		}(i)
	}

	wg.Wait()
	close(results)
	close(errors)

	for err := range errors {
		t.Errorf("Concurrent LoadBalancer() error: %v", err)
	}

	for url := range results {
		if url != "http://test1.com/stream" {
			t.Errorf("Concurrent LoadBalancer() got unexpected URL: %v", url)
		}
	}
}

func TestEdgeCaseURLConfigurations(t *testing.T) {
	urls := safemap.New[string, map[string]string]()
	urls.Set("1", map[string]string{})
	tests := []struct {
		name      string
		streams   map[string]*sourceproc.StreamInfo
		expectErr bool
	}{
		{
			name: "empty URL map",
			streams: map[string]*sourceproc.StreamInfo{
				"test-stream": {
					Title: "Test Stream",
					URLs:  safemap.New[string, map[string]string](),
				},
			},
			expectErr: true,
		},
		{
			name: "nil URL map",
			streams: map[string]*sourceproc.StreamInfo{
				"test-stream": {
					Title: "Test Stream",
					URLs:  nil,
				},
			},
			expectErr: true,
		},
		{
			name: "empty sub-index map",
			streams: map[string]*sourceproc.StreamInfo{
				"test-stream": {
					Title: "Test Stream",
					URLs:  urls,
				},
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			slugParser := &mockSlugParser{
				streams: tt.streams,
			}

			cm := store.NewConcurrencyManager()
			cfg := NewDefaultLBConfig()

			i := NewLoadBalancerInstance(
				cm,
				cfg,
				WithSlugParser(slugParser),
			)

			err := i.fetchBackendUrls("test-stream")

			if (err != nil) != tt.expectErr {
				t.Errorf("NewLoadBalancerInstance() error = %v, wantErr %v", err, tt.expectErr)
			}
		})
	}
}

func TestSessionStatePersistence(t *testing.T) {
	instance, client, _ := setupTestInstance(t)

	// Set up a sequence of failures
	client.errors = map[string]error{
		"http://test1.com/stream": errors.New("failure 1"),
		"http://test1.com/backup": errors.New("failure 2"),
	}
	client.responses["http://test2.com/stream"] = &http.Response{
		StatusCode: 200,
	}

	ctx := context.Background()

	// First request should try and fail with index 1
	req := newTestRequest(http.MethodGet)
	_, err := instance.Balance(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	streamId := instance.GetStreamId(req)

	// Verify session state
	if instance.GetNumTestedIndexes(streamId) != 2 {
		t.Errorf("Expected 2 tested indexes, got %d", instance.GetNumTestedIndexes(streamId))
	}

	// Second request should use index 2 directly
	req = newTestRequest(http.MethodGet)
	result2, err := instance.Balance(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	streamId = instance.GetStreamId(req)

	if result2.Index != "2" {
		t.Errorf("Expected index 2, got %s", result2.Index)
	}

	// Clear session state
	instance.clearTested(streamId)

	// Should start over with index 1 but fail and move to index 2
	req = newTestRequest(http.MethodGet)
	result3, err := instance.Balance(ctx, req)
	if err != nil {
		t.Fatal(err)
	}

	if result3.Index != "2" {
		t.Errorf("Expected index 2, got %s", result3.Index)
	}
}

type mockHTTPClientWithTracking struct {
	responses map[string]*http.Response
	errors    map[string]error
	attempts  []string
	mu        sync.RWMutex
}

func (m *mockHTTPClientWithTracking) Do(req *http.Request) (*http.Response, error) {
	m.mu.Lock()
	m.attempts = append(m.attempts, req.URL.String())
	m.mu.Unlock()

	if err := m.errors[req.URL.String()]; err != nil {
		return nil, err
	}

	resp := m.responses[req.URL.String()]
	if resp == nil {
		return &http.Response{
			StatusCode: http.StatusNotFound,
		}, nil
	}

	return resp, nil
}

func TestLoadBalancerConcurrencyPriority(t *testing.T) {
	urls := safemap.New[string, map[string]string]()
	urls.Set("1", map[string]string{
		"a": "http://index1.com/stream",
	})
	urls.Set("2", map[string]string{
		"a": "http://index2.com/stream",
	})
	urls.Set("3", map[string]string{
		"a": "http://index3.com/stream",
	})
	tests := []struct {
		name           string
		setupEnv       func()
		setupStreams   func() map[string]*sourceproc.StreamInfo
		setupResponses func(client *mockHTTPClientWithTracking)
		manipulateCM   func(*store.ConcurrencyManager)
		expectedOrder  []string
	}{
		{
			name: "tries indexes in order of available slots",
			setupEnv: func() {
				os.Setenv("M3U_MAX_CONCURRENCY_1", "3")
				os.Setenv("M3U_MAX_CONCURRENCY_2", "2")
				os.Setenv("M3U_MAX_CONCURRENCY_3", "1")
			},
			setupStreams: func() map[string]*sourceproc.StreamInfo {
				return map[string]*sourceproc.StreamInfo{
					"test-stream": {
						Title: "Test Stream",
						URLs:  urls,
					},
				}
			},
			setupResponses: func(client *mockHTTPClientWithTracking) {
				client.responses = make(map[string]*http.Response)
				client.errors = map[string]error{
					"http://index1.com/stream": errors.New("failed"),
					"http://index2.com/stream": errors.New("failed"),
					"http://index3.com/stream": errors.New("failed"),
				}
			},
			manipulateCM: nil,
			expectedOrder: []string{
				"http://index1.com/stream", // Priority 3 (3-0)
				"http://index2.com/stream", // Priority 2 (2-0)
				"http://index3.com/stream", // Priority 1 (1-0)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup environment
			if tt.setupEnv != nil {
				tt.setupEnv()
				t.Log("Environment variables set")
			}

			// Setup client
			client := &mockHTTPClientWithTracking{
				responses: make(map[string]*http.Response),
				errors:    make(map[string]error),
				attempts:  make([]string, 0),
			}
			t.Log("Client initialized")

			// Setup streams and responses
			streams := tt.setupStreams()
			tt.setupResponses(client)
			slugParser := &mockSlugParser{streams: streams}
			t.Log("Streams and responses set up")

			// Create indexes slice
			var indexes []string
			streams["test-stream"].URLs.ForEach(func(idx string, _ map[string]string) bool {
				indexes = append(indexes, idx)
				return true
			})
			sort.Strings(indexes) // Ensure consistent order
			t.Logf("Indexes created: %v", indexes)

			// Setup concurrency manager
			cm := store.NewConcurrencyManager()
			if tt.manipulateCM != nil {
				tt.manipulateCM(cm)
				t.Log("Concurrency manager manipulated")
			}

			// Create instance
			cfg := NewDefaultLBConfig()
			cfg.MaxRetries = 1
			instance := NewLoadBalancerInstance(
				cm,
				cfg,
				WithHTTPClient(client),
				WithLogger(logger.Default),
				WithIndexProvider(&mockIndexProvider{indexes: indexes}),
				WithSlugParser(slugParser),
			)

			err := instance.fetchBackendUrls("test-stream")
			if err != nil {
				t.Fatalf("Failed to create LoadBalancerInstance: %v", err)
			}
			t.Log("LoadBalancer instance created")

			// Run balance
			ctx := context.Background()
			result, err := instance.Balance(ctx, newTestRequest(http.MethodGet))
			t.Logf("Balance result: %+v, error: %v", result, err)
			t.Logf("Attempts made: %v", client.attempts)
			t.Logf("Expected order: %v", tt.expectedOrder)

			// Verify attempts
			for i, attempt := range client.attempts {
				if i >= len(tt.expectedOrder) {
					t.Errorf("More attempts than expected. Got attempt %d: %s", i+1, attempt)
					continue
				}
				expected := tt.expectedOrder[i]
				if attempt != expected {
					t.Errorf("Attempt %d: got %s, want %s", i+1, attempt, expected)
				}
			}

			// If we got an error, verify we tried all URLs
			if err != nil && len(client.attempts) != len(tt.expectedOrder) {
				t.Errorf("With error response, got %d attempts, want %d attempts", len(client.attempts), len(tt.expectedOrder))
			}

			// Cleanup
			os.Unsetenv("M3U_MAX_CONCURRENCY_1")
			os.Unsetenv("M3U_MAX_CONCURRENCY_2")
			os.Unsetenv("M3U_MAX_CONCURRENCY_3")
		})
	}
}
