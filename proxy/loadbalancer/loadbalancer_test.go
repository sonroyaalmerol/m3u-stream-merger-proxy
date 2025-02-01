package loadbalancer

import (
	"context"
	"errors"
	"fmt"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/store"
	"net/http"
	"sync"
	"testing"
	"time"
)

// Mock implementations
type mockSlugParser struct {
	streams map[string]store.StreamInfo
}

func (m *mockSlugParser) GetStreamBySlug(slug string) (store.StreamInfo, error) {
	if info, ok := m.streams[slug]; ok {
		return info, nil
	}
	return store.StreamInfo{}, errors.New("stream not found")
}

type mockHTTPClient struct {
	responses map[string]*http.Response
	errors    map[string]error
	delay     time.Duration
	mu        sync.RWMutex
}

func (m *mockHTTPClient) Do(req *http.Request) (*http.Response, error) {
	// Print the request method and URL for debugging
	fmt.Printf("Mock client received request: %s %s\n", req.Method, req.URL.String())

	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}

	// If we have an error for this URL, return it
	if err := m.errors[req.URL.String()]; err != nil {
		return nil, err
	}

	// Get the response for this URL
	resp := m.responses[req.URL.String()]
	if resp == nil {
		// Return a default 404 response instead of nil
		return &http.Response{
			StatusCode: http.StatusNotFound,
		}, nil
	}

	// Return a new response object with same status code to avoid any potential issues with reuse
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

// Test helpers
func setupTestInstance(t *testing.T) (*LoadBalancerInstance, *mockHTTPClient, *mockIndexProvider) {
	client := &mockHTTPClient{
		responses: make(map[string]*http.Response),
		errors:    make(map[string]error),
		delay:     0,
	}

	// Initialize default response
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

	slugParser := &mockSlugParser{
		streams: map[string]store.StreamInfo{
			"test-stream": {
				Title: "Test Stream",
				URLs: map[string]map[string]string{
					"1": {
						"a": "http://test1.com/stream",
						"b": "http://test1.com/backup",
					},
					"2": {
						"a": "http://test2.com/stream",
					},
				},
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
	slugParser := &mockSlugParser{
		streams: map[string]store.StreamInfo{
			"test-stream": {
				Title: "Test Stream",
				URLs: map[string]map[string]string{
					"1": {"a": "http://test1.com/stream"},
				},
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
				// Clear any existing responses/errors
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
				// Clear any existing responses/errors
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
			name: "fallback to different index",
			setupMocks: func(client *mockHTTPClient, provider *mockIndexProvider) {
				// Clear any existing responses/errors
				client.responses = make(map[string]*http.Response)
				client.errors = make(map[string]error)

				client.errors["http://test1.com/stream"] = errors.New("connection failed")
				client.errors["http://test1.com/backup"] = errors.New("connection failed")
				client.responses["http://test2.com/stream"] = &http.Response{
					StatusCode: 200,
				}
			},
			expectErr:      false,
			expectedURL:    "http://test2.com/stream",
			expectedIndex:  "2",
			expectedSubIdx: "a",
		},
		{
			name: "all attempts fail",
			setupMocks: func(client *mockHTTPClient, provider *mockIndexProvider) {
				// Clear any existing responses/errors
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

			session := &store.Session{
				TestedIndexes: []string{},
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			result, err := instance.Balance(ctx, &http.Request{Method: http.MethodGet}, session)

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

func TestLoadBalancerContext(t *testing.T) {
	instance, client, _ := setupTestInstance(t)

	// Set up a mock client with a long delay
	client.delay = 200 * time.Millisecond
	client.responses["http://test1.com/stream"] = &http.Response{
		StatusCode: 200,
	}

	session := &store.Session{
		TestedIndexes: []string{},
	}

	// Use a shorter timeout to ensure we cancel before the response
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := instance.Balance(ctx, &http.Request{Method: http.MethodGet}, session)
	if err == nil {
		t.Error("LoadBalancer() expected error, got nil")
		return
	}

	if err.Error() != "cancelling load balancer" {
		t.Errorf("LoadBalancer() expected 'cancelling load balancer', got %v", err)
	}
}

func TestSessionManagement(t *testing.T) {
	instance, client, _ := setupTestInstance(t)

	// Set up failures for first stream
	client.errors["http://test1.com/stream"] = errors.New("connection failed")
	client.responses["http://test1.com/backup"] = &http.Response{
		StatusCode: 200,
	}

	session := &store.Session{
		TestedIndexes: []string{},
	}

	ctx := context.Background()

	// First attempt should try main stream, fail, and record it
	result, err := instance.Balance(ctx, &http.Request{Method: http.MethodGet}, session)
	if err != nil {
		t.Errorf("LoadBalancer() unexpected error: %v", err)
	}

	// Verify that failed stream was recorded
	testedIndexes := session.GetTestedIndexes()
	if len(testedIndexes) != 1 {
		t.Errorf("Expected 1 tested index, got %d", len(testedIndexes))
	}
	if len(testedIndexes) > 0 && testedIndexes[0] != "1|a" {
		t.Errorf("Expected tested index '1|a', got '%s'", testedIndexes[0])
	}

	// Verify we got the backup stream
	if result.URL != "http://test1.com/backup" {
		t.Errorf("Expected backup URL, got %s", result.URL)
	}
}

func TestLoadBalancerRetries(t *testing.T) {
	instance, client, _ := setupTestInstance(t)

	// Set up temporary failures followed by success
	attemptCount := 0
	client.errors["http://test1.com/stream"] = errors.New("temporary failure")
	client.responses["http://test1.com/backup"] = &http.Response{
		StatusCode: 200,
	}

	session := &store.Session{
		TestedIndexes: []string{},
	}

	ctx := context.Background()
	result, err := instance.Balance(ctx, &http.Request{Method: http.MethodGet}, session)

	if err != nil {
		t.Errorf("LoadBalancer() unexpected error: %v", err)
	}

	if result.URL != "http://test1.com/backup" {
		t.Errorf("LoadBalancer() expected fallback URL, got %v", result.URL)
	}

	if attemptCount > instance.config.MaxRetries {
		t.Errorf("LoadBalancer() made %d attempts, maximum allowed is %d", attemptCount, instance.config.MaxRetries)
	}
}

func TestLoadBalancerWithHTTPStatusCodes(t *testing.T) {
	tests := []struct {
		name        string
		setupMocks  func(*mockHTTPClient)
		expectErr   bool
		expectedURL string
		statusCodes map[string]int
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
			name: "handles 500 server error",
			setupMocks: func(client *mockHTTPClient) {
				client.responses = map[string]*http.Response{
					"http://test1.com/stream": {StatusCode: 500},
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

			session := &store.Session{TestedIndexes: []string{}}
			ctx := context.Background()

			result, err := instance.Balance(ctx, &http.Request{Method: http.MethodGet}, session)

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

			// Clear any existing responses and set up fresh ones
			client.mu.Lock()
			client.responses = make(map[string]*http.Response)
			client.errors = make(map[string]error)

			// Only set up main URL with 200 status
			client.responses["http://test1.com/stream"] = &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
			}

			// Ensure backup URL returns non-200 if accessed
			client.responses["http://test1.com/backup"] = &http.Response{
				StatusCode: http.StatusNotFound,
				Header:     make(http.Header),
			}
			client.mu.Unlock()

			session := &store.Session{TestedIndexes: []string{}}
			ctx := context.Background()

			result, err := instance.Balance(ctx, &http.Request{Method: method}, session)
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

	// Clear any existing responses and set up fresh ones
	client.mu.Lock()
	client.responses = make(map[string]*http.Response)
	client.errors = make(map[string]error)
	// Only set up the main stream URL to return 200
	client.responses["http://test1.com/stream"] = &http.Response{
		StatusCode: http.StatusOK,
	}
	client.mu.Unlock()

	const numGoroutines = 10
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	// Use channels to collect results and errors
	results := make(chan string, numGoroutines)
	errors := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()

			// Create a new session for each goroutine
			session := &store.Session{
				TestedIndexes: []string{},
			}

			ctx := context.Background()
			result, err := instance.Balance(ctx, &http.Request{Method: http.MethodGet}, session)
			if err != nil {
				errors <- fmt.Errorf("goroutine %d error: %v", id, err)
				return
			}
			results <- result.URL
		}(i)
	}

	// Wait for all goroutines to finish
	wg.Wait()
	close(results)
	close(errors)

	// Check for any errors
	for err := range errors {
		t.Errorf("Concurrent LoadBalancer() error: %v", err)
	}

	// Verify all results
	for url := range results {
		if url != "http://test1.com/stream" {
			t.Errorf("Concurrent LoadBalancer() got unexpected URL: %v", url)
		}
	}
}

func TestEdgeCaseURLConfigurations(t *testing.T) {
	tests := []struct {
		name      string
		streams   map[string]store.StreamInfo
		expectErr bool
	}{
		{
			name: "empty URL map",
			streams: map[string]store.StreamInfo{
				"test-stream": {
					Title: "Test Stream",
					URLs:  map[string]map[string]string{},
				},
			},
			expectErr: true,
		},
		{
			name: "nil URL map",
			streams: map[string]store.StreamInfo{
				"test-stream": {
					Title: "Test Stream",
					URLs:  nil,
				},
			},
			expectErr: true,
		},
		{
			name: "empty sub-index map",
			streams: map[string]store.StreamInfo{
				"test-stream": {
					Title: "Test Stream",
					URLs: map[string]map[string]string{
						"1": {},
					},
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

	session := &store.Session{TestedIndexes: []string{}}
	ctx := context.Background()

	// First request should try and fail with index 1
	_, err := instance.Balance(ctx, &http.Request{Method: http.MethodGet}, session)
	if err != nil {
		t.Fatal(err)
	}

	// Verify session state
	if len(session.TestedIndexes) != 2 {
		t.Errorf("Expected 2 tested indexes, got %d", len(session.TestedIndexes))
	}

	// Second request should use index 2 directly
	result2, err := instance.Balance(ctx, &http.Request{Method: http.MethodGet}, session)
	if err != nil {
		t.Fatal(err)
	}

	if result2.Index != "2" {
		t.Errorf("Expected index 2, got %s", result2.Index)
	}

	// Clear session state
	session.TestedIndexes = []string{}

	// Should start over with index 1
	result3, err := instance.Balance(ctx, &http.Request{Method: http.MethodGet}, session)
	if err != nil {
		t.Fatal(err)
	}

	if result3.Index != "2" {
		t.Errorf("Expected index 2, got %s", result3.Index)
	}
}
