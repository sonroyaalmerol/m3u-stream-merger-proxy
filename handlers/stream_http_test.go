package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/proxy/client"
	"m3u-stream-merger/proxy/loadbalancer"
	"m3u-stream-merger/proxy/stream/buffer"
	"m3u-stream-merger/proxy/stream/config"
	"m3u-stream-merger/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"math/rand"
)

type mockStreamManager struct {
	loadBalancerFunc func(ctx context.Context, req *http.Request) (*loadbalancer.LoadBalancerResult, error)
	proxyStreamFunc  func(ctx context.Context, coordinator *buffer.StreamCoordinator, lbRes *loadbalancer.LoadBalancerResult, sClient *client.StreamClient, exitStatus chan<- int)
	getCmFunc        func() *store.ConcurrencyManager
	getRegistryFunc  func() *buffer.StreamRegistry
}

func (m *mockStreamManager) LoadBalancer(ctx context.Context, req *http.Request) (*loadbalancer.LoadBalancerResult, error) {
	if m.loadBalancerFunc != nil {
		return m.loadBalancerFunc(ctx, req)
	}
	return nil, errors.New("loadBalancerFunc not implemented")
}

func (m *mockStreamManager) ProxyStream(ctx context.Context, coordinator *buffer.StreamCoordinator, lbRes *loadbalancer.LoadBalancerResult, sClient *client.StreamClient, exitStatus chan<- int) {
	if m.proxyStreamFunc != nil {
		m.proxyStreamFunc(ctx, coordinator, lbRes, sClient, exitStatus)
	}
}

func (m *mockStreamManager) GetConcurrencyManager() *store.ConcurrencyManager {
	if m.getCmFunc != nil {
		return m.getCmFunc()
	}

	return nil
}

func (m *mockStreamManager) GetStreamRegistry() *buffer.StreamRegistry {
	if m.getRegistryFunc != nil {
		return m.getRegistryFunc()
	}

	return nil
}

func mockResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    &http.Request{},
	}
}

func TestStreamHTTPHandler_ServeHTTP(t *testing.T) {
	cm := store.NewConcurrencyManager()
	config := config.NewDefaultStreamConfig()
	config.ChunkSize = 1024 * 1024
	coordinator := buffer.NewStreamRegistry(config, cm, logger.Default, time.Second)
	coordinator.Unrestrict = true

	tests := []struct {
		name           string
		path           string
		setupMocks     func() *mockStreamManager
		expectedStatus int
		expectedError  bool
	}{
		{
			name: "successful stream with complete playback",
			path: "/test.m3u8",
			setupMocks: func() *mockStreamManager {
				manager := &mockStreamManager{}

				manager.loadBalancerFunc = func(ctx context.Context, req *http.Request) (*loadbalancer.LoadBalancerResult, error) {
					return &loadbalancer.LoadBalancerResult{Response: mockResponse(http.StatusOK, "test content"), URL: "http://example.com", Index: "1", SubIndex: "1"}, nil
				}

				manager.proxyStreamFunc = func(ctx context.Context, coordinator *buffer.StreamCoordinator, lbRes *loadbalancer.LoadBalancerResult, sClient *client.StreamClient, exitStatus chan<- int) {
					_ = sClient.WriteHeader(lbRes.Response.StatusCode)
					exitStatus <- proxy.StatusM3U8Parsed // Normal completion
				}

				manager.getCmFunc = func() *store.ConcurrencyManager {
					return cm
				}

				manager.getRegistryFunc = func() *buffer.StreamRegistry {
					return coordinator
				}

				return manager
			},
			expectedStatus: http.StatusOK,
			expectedError:  false,
		},
		{
			name: "stream with retry and eventual success",
			path: "/test.m3u8",
			setupMocks: func() *mockStreamManager {
				manager := &mockStreamManager{}

				callCount := 0
				manager.loadBalancerFunc = func(ctx context.Context, req *http.Request) (*loadbalancer.LoadBalancerResult, error) {
					callCount++
					if callCount == 1 {
						return &loadbalancer.LoadBalancerResult{Response: mockResponse(http.StatusOK, "retry content"), URL: "http://retry.com", Index: "1", SubIndex: "1"}, nil
					}
					return &loadbalancer.LoadBalancerResult{Response: mockResponse(http.StatusOK, "success content"), URL: "http://success.com", Index: "2", SubIndex: "2"}, nil
				}

				firstCall := true
				manager.proxyStreamFunc = func(ctx context.Context, coordinator *buffer.StreamCoordinator, lbRes *loadbalancer.LoadBalancerResult, sClient *client.StreamClient, exitStatus chan<- int) {
					_ = sClient.WriteHeader(lbRes.Response.StatusCode)
					if firstCall {
						firstCall = false
						exitStatus <- proxy.StatusM3U8ParseError // Trigger retry
					} else {
						exitStatus <- proxy.StatusM3U8Parsed // Success on retry
					}
				}

				manager.getCmFunc = func() *store.ConcurrencyManager {
					return cm
				}

				manager.getRegistryFunc = func() *buffer.StreamRegistry {
					return coordinator
				}

				return manager
			},
			expectedStatus: http.StatusOK,
			expectedError:  false,
		},
		{
			name: "stream with context timeout",
			path: "/test.m3u8",
			setupMocks: func() *mockStreamManager {
				manager := &mockStreamManager{}

				manager.loadBalancerFunc = func(ctx context.Context, req *http.Request) (*loadbalancer.LoadBalancerResult, error) {
					return &loadbalancer.LoadBalancerResult{Response: mockResponse(http.StatusOK, "timeout content"), URL: "http://timeout.com", Index: "1", SubIndex: "1"}, nil
				}

				manager.proxyStreamFunc = func(ctx context.Context, coordinator *buffer.StreamCoordinator, lbRes *loadbalancer.LoadBalancerResult, sClient *client.StreamClient, exitStatus chan<- int) {
					// Simulate a long operation that gets cancelled
					_ = sClient.WriteHeader(lbRes.Response.StatusCode)
					select {
					case <-ctx.Done():
						exitStatus <- proxy.StatusClientClosed
					case <-time.After(100 * time.Millisecond):
						exitStatus <- proxy.StatusM3U8Parsed
					}
				}

				manager.getCmFunc = func() *store.ConcurrencyManager {
					return cm
				}

				manager.getRegistryFunc = func() *buffer.StreamRegistry {
					return coordinator
				}

				return manager
			},
			expectedStatus: http.StatusOK, // The response headers are still sent
			expectedError:  false,
		},
		{
			name: "invalid stream URL with special characters",
			path: "/test@#$.m3u8",
			setupMocks: func() *mockStreamManager {
				manager := &mockStreamManager{}

				manager.getRegistryFunc = func() *buffer.StreamRegistry {
					return coordinator
				}

				manager.proxyStreamFunc = func(ctx context.Context, coordinator *buffer.StreamCoordinator, lbRes *loadbalancer.LoadBalancerResult, sClient *client.StreamClient, exitStatus chan<- int) {
					_ = sClient.WriteHeader(lbRes.Response.StatusCode)
				}

				return manager
			},
			expectedStatus: http.StatusOK,
			expectedError:  false,
		},
		{
			name: "load balancer network error",
			path: "/test.m3u8",
			setupMocks: func() *mockStreamManager {
				manager := &mockStreamManager{}

				manager.loadBalancerFunc = func(ctx context.Context, req *http.Request) (*loadbalancer.LoadBalancerResult, error) {
					return nil, errors.New("network connection error")
				}

				manager.getCmFunc = func() *store.ConcurrencyManager {
					return cm
				}

				manager.proxyStreamFunc = func(ctx context.Context, coordinator *buffer.StreamCoordinator, lbRes *loadbalancer.LoadBalancerResult, sClient *client.StreamClient, exitStatus chan<- int) {
					_ = sClient.WriteHeader(lbRes.Response.StatusCode)
				}

				manager.getRegistryFunc = func() *buffer.StreamRegistry {
					return coordinator
				}

				return manager
			},
			expectedStatus: http.StatusOK,
			expectedError:  true,
		},
		{
			name: "stream with server error response",
			path: "/test",
			setupMocks: func() *mockStreamManager {
				manager := &mockStreamManager{}

				manager.loadBalancerFunc = func(ctx context.Context, req *http.Request) (*loadbalancer.LoadBalancerResult, error) {
					return &loadbalancer.LoadBalancerResult{Response: mockResponse(http.StatusInternalServerError, "server error"), URL: "http://error.com", Index: "1", SubIndex: "1"}, nil
				}

				manager.proxyStreamFunc = func(ctx context.Context, coordinator *buffer.StreamCoordinator, lbRes *loadbalancer.LoadBalancerResult, sClient *client.StreamClient, exitStatus chan<- int) {
					_ = sClient.WriteHeader(lbRes.Response.StatusCode)
				}

				manager.getCmFunc = func() *store.ConcurrencyManager {
					return cm
				}

				manager.getRegistryFunc = func() *buffer.StreamRegistry {
					return coordinator
				}

				return manager
			},
			expectedStatus: http.StatusInternalServerError,
			expectedError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := tt.setupMocks()
			handler := NewStreamHTTPHandler(manager, logger.Default)

			// Create a cancellable context for timeout tests
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()

			req := httptest.NewRequest(http.MethodGet, tt.path, nil).WithContext(ctx)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			// Additional assertions based on test case
			switch tt.name {
			case "stream with retry and eventual success":
				// Could add assertions about retry behavior
			case "stream with EOF condition":
				if w.Header().Get("Content-Type") != "application/vnd.apple.mpegurl" {
					t.Error("content type header not properly set for M3U8")
				}
			}
		})
	}
}

func TestStreamHTTPHandler_DisconnectionConcurrency(t *testing.T) {
	cm := store.NewConcurrencyManager()
	config := config.NewDefaultStreamConfig()
	config.ChunkSize = 1024 * 1024
	coordinator := buffer.NewStreamRegistry(config, cm, logger.Default, time.Second)
	coordinator.Unrestrict = true

	tests := []struct {
		name         string
		setupMocks   func() *mockStreamManager
		numRequests  int
		validateFunc func(t *testing.T, cm *store.ConcurrencyManager)
	}{
		{
			name: "random client disconnections",
			setupMocks: func() *mockStreamManager {
				manager := &mockStreamManager{}

				manager.loadBalancerFunc = func(ctx context.Context, req *http.Request) (*loadbalancer.LoadBalancerResult, error) {
					return &loadbalancer.LoadBalancerResult{Response: mockResponse(http.StatusOK, "test content"), URL: "http://example.com", Index: "1", SubIndex: "m3u_1"}, nil
				}

				manager.proxyStreamFunc = func(ctx context.Context, coordinator *buffer.StreamCoordinator, lbRes *loadbalancer.LoadBalancerResult, sClient *client.StreamClient, exitStatus chan<- int) {
					_ = sClient.WriteHeader(lbRes.Response.StatusCode)
					// Randomly decide between normal completion, client disconnect, or EOF
					time.Sleep(time.Duration(rand.Intn(100)) * time.Millisecond)
					outcomes := []int{
						proxy.StatusM3U8Parsed,
						proxy.StatusClientClosed,
						proxy.StatusEOF,
					}
					exitStatus <- outcomes[rand.Intn(len(outcomes))]
				}

				manager.getCmFunc = func() *store.ConcurrencyManager {
					return cm
				}

				manager.getRegistryFunc = func() *buffer.StreamRegistry {
					return coordinator
				}

				return manager
			},
			numRequests: 10,
			validateFunc: func(t *testing.T, cm *store.ConcurrencyManager) {
				// Wait for all connections to finish
				time.Sleep(200 * time.Millisecond)
				current, _, _ := cm.GetConcurrencyStatus("m3u_1")
				if current != 0 {
					t.Errorf("concurrency count not zero after all disconnections: got %d", current)
				}
			},
		},
		{
			name: "simultaneous disconnections",
			setupMocks: func() *mockStreamManager {
				manager := &mockStreamManager{}

				var streamWG sync.WaitGroup
				streamWG.Add(5) // Wait for 5 streams to be active

				manager.loadBalancerFunc = func(ctx context.Context, req *http.Request) (*loadbalancer.LoadBalancerResult, error) {
					return &loadbalancer.LoadBalancerResult{Response: mockResponse(http.StatusOK, "test content"), URL: "http://example.com", Index: "1", SubIndex: "m3u_2"}, nil
				}

				manager.proxyStreamFunc = func(ctx context.Context, coordinator *buffer.StreamCoordinator, lbRes *loadbalancer.LoadBalancerResult, sClient *client.StreamClient, exitStatus chan<- int) {
					_ = sClient.WriteHeader(lbRes.Response.StatusCode)
					streamWG.Done()
					streamWG.Wait()                        // Wait for all streams to be ready
					exitStatus <- proxy.StatusClientClosed // All disconnect at once
				}

				manager.getCmFunc = func() *store.ConcurrencyManager {
					return cm
				}

				manager.getRegistryFunc = func() *buffer.StreamRegistry {
					return coordinator
				}

				return manager
			},
			numRequests: 5,
			validateFunc: func(t *testing.T, cm *store.ConcurrencyManager) {
				time.Sleep(200 * time.Millisecond)
				current, _, _ := cm.GetConcurrencyStatus("m3u_2")
				if current != 0 {
					t.Errorf("concurrency count not zero after simultaneous disconnections: got %d", current)
				}
			},
		},
		{
			name: "mixed completion statuses with retries",
			setupMocks: func() *mockStreamManager {
				manager := &mockStreamManager{}

				completionCount := int32(0)

				manager.loadBalancerFunc = func(ctx context.Context, req *http.Request) (*loadbalancer.LoadBalancerResult, error) {
					count := atomic.AddInt32(&completionCount, 1)
					// Alternate between different m3u indices to test cross-stream impacts
					m3uIndex := fmt.Sprintf("m3u_%d", count%2+3)
					return &loadbalancer.LoadBalancerResult{Response: mockResponse(http.StatusOK, "test content"), URL: "http://example.com", Index: "1", SubIndex: m3uIndex}, nil
				}

				manager.proxyStreamFunc = func(ctx context.Context, coordinator *buffer.StreamCoordinator, lbRes *loadbalancer.LoadBalancerResult, sClient *client.StreamClient, exitStatus chan<- int) {
					_ = sClient.WriteHeader(lbRes.Response.StatusCode)
					time.Sleep(time.Duration(rand.Intn(50)) * time.Millisecond)

					// Mix of different completion statuses including retry scenarios
					outcomes := []int{
						proxy.StatusM3U8Parsed,
						proxy.StatusClientClosed,
						proxy.StatusM3U8ParseError, // This should trigger retry
						proxy.StatusEOF,
					}

					status := outcomes[rand.Intn(len(outcomes))]
					exitStatus <- status

					// If it was a parse error, simulate retry behavior
					if status == proxy.StatusM3U8ParseError {
						time.Sleep(10 * time.Millisecond)
						exitStatus <- proxy.StatusM3U8Parsed
					}
				}

				manager.getCmFunc = func() *store.ConcurrencyManager {
					return cm
				}

				manager.getRegistryFunc = func() *buffer.StreamRegistry {
					return coordinator
				}

				return manager
			},
			numRequests: 8,
			validateFunc: func(t *testing.T, cm *store.ConcurrencyManager) {
				// Check both m3u indices used in the test
				time.Sleep(300 * time.Millisecond) // Allow for retries to complete

				current3, _, _ := cm.GetConcurrencyStatus("m3u_3")
				current4, _, _ := cm.GetConcurrencyStatus("m3u_4")

				if current3 != 0 || current4 != 0 {
					t.Errorf("concurrency counts not zero after mixed completions: m3u_3=%d, m3u_4=%d",
						current3, current4)
				}
			},
		},
		{
			name: "rapid connect/disconnect cycles",
			setupMocks: func() *mockStreamManager {
				manager := &mockStreamManager{}

				manager.loadBalancerFunc = func(ctx context.Context, req *http.Request) (*loadbalancer.LoadBalancerResult, error) {
					return &loadbalancer.LoadBalancerResult{Response: mockResponse(http.StatusOK, "test content"), URL: "http://example.com", Index: "1", SubIndex: "m3u_5"}, nil
				}

				manager.proxyStreamFunc = func(ctx context.Context, coordinator *buffer.StreamCoordinator, lbRes *loadbalancer.LoadBalancerResult, sClient *client.StreamClient, exitStatus chan<- int) {
					_ = sClient.WriteHeader(lbRes.Response.StatusCode)
					// Very short-lived connections
					time.Sleep(time.Duration(rand.Intn(10)) * time.Millisecond)
					exitStatus <- proxy.StatusClientClosed
				}

				manager.getCmFunc = func() *store.ConcurrencyManager {
					return cm
				}

				manager.getRegistryFunc = func() *buffer.StreamRegistry {
					return coordinator
				}

				return manager
			},
			numRequests: 20, // More requests to stress test
			validateFunc: func(t *testing.T, cm *store.ConcurrencyManager) {
				time.Sleep(100 * time.Millisecond)
				current, _, _ := cm.GetConcurrencyStatus("m3u_5")

				// Take multiple readings to ensure stability
				for i := 0; i < 3; i++ {
					time.Sleep(50 * time.Millisecond)
					newCurrent, _, _ := cm.GetConcurrencyStatus("m3u_5")
					if newCurrent != current {
						t.Errorf("concurrency count unstable: changed from %d to %d", current, newCurrent)
					}
					if current != 0 {
						t.Errorf("concurrency count not zero after rapid cycles: got %d", current)
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := tt.setupMocks()
			handler := NewStreamHTTPHandler(manager, logger.Default)

			var wg sync.WaitGroup
			wg.Add(tt.numRequests)

			// Launch concurrent requests
			for i := 0; i < tt.numRequests; i++ {
				go func() {
					defer wg.Done()

					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					defer cancel()

					req := httptest.NewRequest(http.MethodGet, "/test.m3u8", nil).WithContext(ctx)
					w := httptest.NewRecorder()

					handler.ServeHTTP(w, req)
				}()
			}

			wg.Wait()

			if tt.validateFunc != nil {
				tt.validateFunc(t, manager.GetConcurrencyManager())
			}
		})
	}
}
