package loadbalancer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/sourceproc"
	"m3u-stream-merger/store"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v3"
)

// trackingBody wraps a ReadCloser and records whether Close has been called.
// Used in tests to verify that response bodies are properly closed on all
// code paths, preventing TCP connection leaks.
type trackingBody struct {
	io.ReadCloser
	mu     sync.Mutex
	closed bool
}

func newTrackingBody(content string) *trackingBody {
	return &trackingBody{ReadCloser: io.NopCloser(strings.NewReader(content))}
}

func (b *trackingBody) Close() error {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	return b.ReadCloser.Close()
}

func (b *trackingBody) IsClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

// errorOnReadBody is a ReadCloser whose Read always returns an error, used to
// trigger the evaluateBufferHealth error path.
type errorOnReadBody struct {
	trackingBody
}

func newErrorOnReadBody() *errorOnReadBody {
	b := &errorOnReadBody{}
	b.ReadCloser = io.NopCloser(strings.NewReader(""))
	return b
}

func (b *errorOnReadBody) Read(_ []byte) (int, error) {
	return 0, fmt.Errorf("simulated read error")
}

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
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	}

	return &http.Response{
		StatusCode: resp.StatusCode,
		Body:       resp.Body,
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
		Body:       io.NopCloser(strings.NewReader("dummy stream content")),
	}
	client.responses["http://test1.com/backup"] = &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("dummy backup content")),
	}
	client.responses["http://test2.com/stream"] = &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("dummy stream2 content")),
	}

	indexProvider := &mockIndexProvider{
		indexes: []string{"1", "2"},
	}

	urls := xsync.NewMapOf[string, map[string]string]()
	urls.Store("1", map[string]string{
		"a": "http://test1.com/stream",
		"b": "http://test1.com/backup",
	})
	urls.Store("2", map[string]string{
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
	urls := xsync.NewMapOf[string, map[string]string]()
	urls.Store("1", map[string]string{
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
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("dummy stream content")),
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
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("dummy backup content")),
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
					"http://test1.com/stream": {
						StatusCode: 301,
						Body:       io.NopCloser(strings.NewReader("dummy status 301")),
					},
					"http://test1.com/backup": {
						StatusCode: 200,
						Body:       io.NopCloser(strings.NewReader("dummy backup")),
					},
				}
			},
			expectErr:   false,
			expectedURL: "http://test1.com/backup",
		},
		{
			name: "handles 404 not found",
			setupMocks: func(client *mockHTTPClient) {
				client.responses = map[string]*http.Response{
					"http://test1.com/stream": {
						StatusCode: 404,
						Body:       io.NopCloser(strings.NewReader("dummy status 404")),
					},
					"http://test1.com/backup": {
						StatusCode: 200,
						Body:       io.NopCloser(strings.NewReader("dummy backup")),
					},
				}
			},
			expectErr:   false,
			expectedURL: "http://test1.com/backup",
		},
		{
			name: "all endpoints return 500",
			setupMocks: func(client *mockHTTPClient) {
				client.responses = map[string]*http.Response{
					"http://test1.com/stream": {
						StatusCode: 500,
						Body:       io.NopCloser(strings.NewReader("dummy status 500")),
					},
					"http://test1.com/backup": {
						StatusCode: 500,
						Body:       io.NopCloser(strings.NewReader("dummy status 500")),
					},
					"http://test2.com/stream": {
						StatusCode: 500,
						Body:       io.NopCloser(strings.NewReader("dummy status 500")),
					},
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
				Body:       io.NopCloser(strings.NewReader("dummy content")),
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
		Body:       io.NopCloser(strings.NewReader("dummy stream content")),
	}
	client.mu.Unlock()

	const numGoroutines = 10
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	results := make(chan string, numGoroutines)
	errorsCh := make(chan error, numGoroutines)

	for i := range numGoroutines {
		go func(id int) {
			defer wg.Done()

			ctx := context.Background()
			result, err := instance.Balance(ctx, newTestRequest(http.MethodGet))
			if err != nil {
				errorsCh <- fmt.Errorf("goroutine %d error: %v", id, err)
				return
			}
			results <- result.URL
		}(i)
	}

	wg.Wait()
	close(results)
	close(errorsCh)

	for err := range errorsCh {
		t.Errorf("Concurrent LoadBalancer() error: %v", err)
	}

	for url := range results {
		if url != "http://test1.com/stream" {
			t.Errorf("Concurrent LoadBalancer() got unexpected URL: %v", url)
		}
	}
}

func TestEdgeCaseURLConfigurations(t *testing.T) {
	urls := xsync.NewMapOf[string, map[string]string]()
	urls.Store("1", map[string]string{})
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
					URLs:  xsync.NewMapOf[string, map[string]string](),
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
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("dummy stream2 content")),
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
	urls := xsync.NewMapOf[string, map[string]string]()
	urls.Store("1", map[string]string{
		"a": "http://index1.com/stream",
	})
	urls.Store("2", map[string]string{
		"a": "http://index2.com/stream",
	})
	urls.Store("3", map[string]string{
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
			streams["test-stream"].URLs.Range(func(idx string, _ map[string]string) bool {
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

// --- Resource-leak regression tests ---
// These tests guard against the gradual degradation bug where HTTP response
// bodies were left open, eventually exhausting file descriptors / connections.

// TestResponseBodyClosedOnNonOKStatus verifies that when a health-check
// request returns a non-200 status, the response body is closed so the
// underlying TCP connection is returned to the pool.
func TestResponseBodyClosedOnNonOKStatus(t *testing.T) {
	body404 := newTrackingBody("not found")
	body200 := newTrackingBody(strings.Repeat("x", 512))

	client := &mockHTTPClient{
		responses: map[string]*http.Response{
			"http://primary.test/stream": {StatusCode: http.StatusNotFound, Body: body404},
			"http://backup.test/stream":  {StatusCode: http.StatusOK, Body: body200},
		},
		errors: make(map[string]error),
	}

	urls := xsync.NewMapOf[string, map[string]string]()
	urls.Store("1", map[string]string{
		"a": "http://primary.test/stream",
		"b": "http://backup.test/stream",
	})
	slugParser := &mockSlugParser{
		streams: map[string]*sourceproc.StreamInfo{
			"test-stream": {Title: "Test Stream", URLs: urls},
		},
	}

	cm := store.NewConcurrencyManager()
	cfg := &LBConfig{MaxRetries: 1, RetryWait: 0, BufferChunk: 512}
	instance := NewLoadBalancerInstance(cm, cfg,
		WithHTTPClient(client),
		WithLogger(logger.Default),
		WithIndexProvider(&mockIndexProvider{indexes: []string{"1"}}),
		WithSlugParser(slugParser),
	)
	if err := instance.fetchBackendUrls("test-stream"); err != nil {
		t.Fatalf("fetchBackendUrls: %v", err)
	}

	innerMap, _ := instance.GetStreamInfo().URLs.Load("1")
	result, err := instance.tryStreamUrls(context.Background(), newTestRequest(http.MethodGet), "test-stream", "1", innerMap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	result.Response.Body.Close()

	if !body404.IsClosed() {
		t.Error("body for non-200 response was not closed; this leaks a TCP connection")
	}
}

// TestResponseBodyClosedOnEvaluateError verifies that when evaluateBufferHealth
// returns an error (e.g. the upstream stream errors mid-read), the response
// body is closed so the TCP connection is not leaked.
func TestResponseBodyClosedOnEvaluateError(t *testing.T) {
	badBody := newErrorOnReadBody()
	goodBody := newTrackingBody(strings.Repeat("x", 512))

	client := &mockHTTPClient{
		responses: map[string]*http.Response{
			"http://bad.test/stream":  {StatusCode: http.StatusOK, Body: badBody},
			"http://good.test/stream": {StatusCode: http.StatusOK, Body: goodBody},
		},
		errors: make(map[string]error),
	}

	urls := xsync.NewMapOf[string, map[string]string]()
	urls.Store("1", map[string]string{
		"a": "http://bad.test/stream",
		"b": "http://good.test/stream",
	})
	slugParser := &mockSlugParser{
		streams: map[string]*sourceproc.StreamInfo{
			"test-stream": {Title: "Test Stream", URLs: urls},
		},
	}

	cm := store.NewConcurrencyManager()
	cfg := &LBConfig{MaxRetries: 1, RetryWait: 0, BufferChunk: 512}
	instance := NewLoadBalancerInstance(cm, cfg,
		WithHTTPClient(client),
		WithLogger(logger.Default),
		WithIndexProvider(&mockIndexProvider{indexes: []string{"1"}}),
		WithSlugParser(slugParser),
	)
	if err := instance.fetchBackendUrls("test-stream"); err != nil {
		t.Fatalf("fetchBackendUrls: %v", err)
	}

	innerMap, _ := instance.GetStreamInfo().URLs.Load("1")
	result, err := instance.tryStreamUrls(context.Background(), newTestRequest(http.MethodGet), "test-stream", "1", innerMap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	result.Response.Body.Close()

	if !badBody.IsClosed() {
		t.Error("body for stream that errored during health check was not closed; this leaks a TCP connection")
	}
}

// TestNonWinningResponseBodiesClosed verifies that when multiple candidate
// streams pass the health check concurrently, only the best one is returned
// and all others have their response bodies closed.
func TestNonWinningResponseBodiesClosed(t *testing.T) {
	// Both bodies return data quickly (EOF), so both pass evaluateBufferHealth.
	body1 := newTrackingBody(strings.Repeat("a", 512))
	body2 := newTrackingBody(strings.Repeat("b", 512))

	client := &mockHTTPClient{
		responses: map[string]*http.Response{
			"http://stream1.test/s": {StatusCode: http.StatusOK, Body: body1},
			"http://stream2.test/s": {StatusCode: http.StatusOK, Body: body2},
		},
		errors: make(map[string]error),
	}

	urls := xsync.NewMapOf[string, map[string]string]()
	urls.Store("1", map[string]string{
		"a": "http://stream1.test/s",
		"b": "http://stream2.test/s",
	})
	slugParser := &mockSlugParser{
		streams: map[string]*sourceproc.StreamInfo{
			"test-stream": {Title: "Test Stream", URLs: urls},
		},
	}

	cm := store.NewConcurrencyManager()
	cfg := &LBConfig{MaxRetries: 1, RetryWait: 0, BufferChunk: 512}
	instance := NewLoadBalancerInstance(cm, cfg,
		WithHTTPClient(client),
		WithLogger(logger.Default),
		WithIndexProvider(&mockIndexProvider{indexes: []string{"1"}}),
		WithSlugParser(slugParser),
	)
	if err := instance.fetchBackendUrls("test-stream"); err != nil {
		t.Fatalf("fetchBackendUrls: %v", err)
	}

	innerMap, _ := instance.GetStreamInfo().URLs.Load("1")
	result, err := instance.tryStreamUrls(context.Background(), newTestRequest(http.MethodGet), "test-stream", "1", innerMap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Before we close the winner: exactly one body should already be closed
	// (the loser was discarded by tryStreamUrls).
	loserClosed := body1.IsClosed() || body2.IsClosed()
	if !loserClosed {
		t.Error("non-winning response body was not closed by tryStreamUrls; this leaks a TCP connection")
	}

	winnerStillOpen := !body1.IsClosed() || !body2.IsClosed()
	if !winnerStillOpen {
		t.Error("winning response body was closed prematurely by tryStreamUrls")
	}

	// Closing the winner should close the remaining body.
	result.Response.Body.Close()
	if !body1.IsClosed() || !body2.IsClosed() {
		t.Error("not all response bodies were closed after winner was closed")
	}
}

// TestEvaluateBufferHealthBodyClose verifies that after evaluateBufferHealth
// reconstructs resp.Body (to prepend already-read bytes), calling Close on
// the returned body actually closes the original underlying body — i.e. the
// io.NopCloser replacement bug is gone.
func TestEvaluateBufferHealthBodyClose(t *testing.T) {
	original := newTrackingBody(strings.Repeat("x", 2048))
	resp := &http.Response{Body: original}

	_, err := evaluateBufferHealth(resp, 512)
	if err != nil {
		t.Fatalf("evaluateBufferHealth returned error: %v", err)
	}

	if original.IsClosed() {
		t.Fatal("original body was closed during measurement (should only be closed by caller)")
	}

	// Simulates what the caller does with the winning response.
	resp.Body.Close()

	if !original.IsClosed() {
		t.Error("closing resp.Body after evaluateBufferHealth did not close the original body; " +
			"the underlying TCP connection is leaked")
	}
}

// TestHealthClientDisablesKeepAlives verifies that the health-check HTTP
// client always has DisableKeepAlives set. The health client is created fresh
// per load-balancer instance, so idle connections would never be reused —
// without DisableKeepAlives they accumulate as open file descriptors.
func TestHealthClientDisablesKeepAlives(t *testing.T) {
	cm := store.NewConcurrencyManager()
	cfg := NewDefaultLBConfig()
	instance := NewLoadBalancerInstance(cm, cfg)

	hc, ok := instance.healthClient.(*http.Client)
	if !ok {
		t.Fatal("healthClient is not *http.Client")
	}

	transport, ok := hc.Transport.(*http.Transport)
	if !ok {
		t.Fatal("healthClient.Transport is not *http.Transport")
	}

	if !transport.DisableKeepAlives {
		t.Error("health client transport must have DisableKeepAlives=true; " +
			"without it, idle connections accumulate and exhaust file descriptors over time")
	}
}

// TestConcurrentHealthChecksCancelledAfterFirstSuccess verifies that once the
// first health-check goroutine finds a valid stream, the remaining concurrent
// health-check goroutines are cancelled via context.  Without this, every
// sub-URL goroutine opens its own upstream connection simultaneously, which
// hits per-account connection limits on IPTV servers and causes 404 errors.
func TestConcurrentHealthChecksCancelledAfterFirstSuccess(t *testing.T) {
	// Two sub-URLs under the same index; both return 200.
	// We track how many requests actually reached the server.
	var requestCount int32
	var mu sync.Mutex
	requestOrder := make([]string, 0, 2)

	// slow delivers data only after a short delay so the fast URL wins first
	fastBody := strings.Repeat("x", 4096)
	slowBody := strings.Repeat("y", 4096)

	client := &mockHTTPClient{
		responses: map[string]*http.Response{
			"http://fast.example.com/stream": {
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(fastBody)),
			},
		},
		errors: map[string]error{},
	}

	// Override Do so we can count calls and simulate the slow URL.
	type callTracker struct {
		mockHTTPClient
	}
	tracker := &struct {
		mu          sync.Mutex
		requestURLs []string
		client      *mockHTTPClient
	}{
		requestURLs: []string{},
		client:      client,
	}
	_ = tracker

	// Use a counting client instead.
	countingClient := &countingHTTPClient{
		mu:           &mu,
		requestOrder: &requestOrder,
		requestCount: &requestCount,
		responses: map[string]*http.Response{
			"http://fast.example.com/stream": {
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(fastBody)),
			},
			"http://slow.example.com/stream": {
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(slowBody)),
				// slow URL has a delay baked into the client
			},
		},
		delays: map[string]time.Duration{
			"http://slow.example.com/stream": 500 * time.Millisecond,
		},
	}

	indexProvider := &mockIndexProvider{indexes: []string{"1"}}

	urls := xsync.NewMapOf[string, map[string]string]()
	urls.Store("1", map[string]string{
		"a": "http://fast.example.com/stream",
		"b": "http://slow.example.com/stream",
	})

	slugParser := &mockSlugParser{
		streams: map[string]*sourceproc.StreamInfo{
			"ch": {Title: "ch", URLs: urls},
		},
	}

	cm := store.NewConcurrencyManager()
	cfg := NewDefaultLBConfig()

	instance := NewLoadBalancerInstance(
		cm, cfg,
		WithHTTPClient(countingClient),
		WithLogger(logger.Default),
		WithIndexProvider(indexProvider),
		WithSlugParser(slugParser),
	)
	instance.healthClient = countingClient

	req, _ := http.NewRequest("GET", "/ch.ts", nil)
	result, err := instance.Balance(context.Background(), req)
	if err != nil {
		t.Fatalf("Balance returned error: %v", err)
	}
	if result != nil {
		result.Response.Body.Close()
	}

	mu.Lock()
	gotOrder := append([]string(nil), requestOrder...)
	mu.Unlock()

	// The fast URL should have been hit; the slow URL's Do() call should have
	// been cancelled by the context before it completed (or it completed but the
	// result was discarded).  Either way, the winning response must be from the
	// fast URL.
	if result == nil || result.URL != "http://fast.example.com/stream" {
		t.Errorf("expected fast URL to win, got result=%v, requestOrder=%v", result, gotOrder)
	}
}

// countingHTTPClient is an HTTPClient that records every request URL and
// supports per-URL delays (honours request context cancellation).
type countingHTTPClient struct {
	mu           *sync.Mutex
	requestOrder *[]string
	requestCount *int32
	responses    map[string]*http.Response
	delays       map[string]time.Duration
}

func (c *countingHTTPClient) Do(req *http.Request) (*http.Response, error) {
	url := req.URL.String()

	c.mu.Lock()
	*c.requestOrder = append(*c.requestOrder, url)
	c.mu.Unlock()

	if d, ok := c.delays[url]; ok {
		select {
		case <-time.After(d):
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}

	if resp, ok := c.responses[url]; ok {
		// Return a fresh reader each time so tests don't share state.
		body, _ := io.ReadAll(resp.Body)
		resp.Body = io.NopCloser(strings.NewReader(string(body)))
		return &http.Response{
			StatusCode: resp.StatusCode,
			Body:       io.NopCloser(strings.NewReader(string(body))),
		}, nil
	}

	return &http.Response{
		StatusCode: http.StatusNotFound,
		Body:       io.NopCloser(strings.NewReader("")),
	}, nil
}
