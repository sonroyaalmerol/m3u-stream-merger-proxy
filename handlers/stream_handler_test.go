package handlers

import (
	"context"
	"errors"
	"io"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type mockStreamManager struct {
	loadBalancerFunc func(ctx context.Context, req *http.Request, session *store.Session) (*http.Response, string, string, string, error)
	proxyStreamFunc  func(ctx context.Context, selectedIndex, selectedSubIndex string, resp *http.Response, r *http.Request, w http.ResponseWriter, exitStatus chan<- int)
}

func (m *mockStreamManager) LoadBalancer(ctx context.Context, req *http.Request, session *store.Session) (*http.Response, string, string, string, error) {
	if m.loadBalancerFunc != nil {
		return m.loadBalancerFunc(ctx, req, session)
	}
	return nil, "", "", "", errors.New("loadBalancerFunc not implemented")
}

func (m *mockStreamManager) ProxyStream(ctx context.Context, selectedIndex, selectedSubIndex string, resp *http.Response, r *http.Request, w http.ResponseWriter, exitStatus chan<- int) {
	if m.proxyStreamFunc != nil {
		m.proxyStreamFunc(ctx, selectedIndex, selectedSubIndex, resp, r, w, exitStatus)
	}
}

func mockResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    &http.Request{},
	}
}

func TestStreamHandler_ServeHTTP(t *testing.T) {
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

				manager.loadBalancerFunc = func(ctx context.Context, req *http.Request, session *store.Session) (*http.Response, string, string, string, error) {
					return mockResponse(http.StatusOK, "test content"), "http://example.com", "1", "1", nil
				}

				manager.proxyStreamFunc = func(ctx context.Context, selectedIndex, selectedSubIndex string, resp *http.Response, r *http.Request, w http.ResponseWriter, exitStatus chan<- int) {
					exitStatus <- proxy.StatusM3U8Parsed // Normal completion
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
				manager.loadBalancerFunc = func(ctx context.Context, req *http.Request, session *store.Session) (*http.Response, string, string, string, error) {
					callCount++
					if callCount == 1 {
						return mockResponse(http.StatusOK, "retry content"), "http://retry.com", "1", "1", nil
					}
					return mockResponse(http.StatusOK, "success content"), "http://success.com", "2", "2", nil
				}

				firstCall := true
				manager.proxyStreamFunc = func(ctx context.Context, selectedIndex, selectedSubIndex string, resp *http.Response, r *http.Request, w http.ResponseWriter, exitStatus chan<- int) {
					if firstCall {
						firstCall = false
						exitStatus <- proxy.StatusM3U8ParseError // Trigger retry
					} else {
						exitStatus <- proxy.StatusM3U8Parsed // Success on retry
					}
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

				manager.loadBalancerFunc = func(ctx context.Context, req *http.Request, session *store.Session) (*http.Response, string, string, string, error) {
					return mockResponse(http.StatusOK, "timeout content"), "http://timeout.com", "1", "1", nil
				}

				manager.proxyStreamFunc = func(ctx context.Context, selectedIndex, selectedSubIndex string, resp *http.Response, r *http.Request, w http.ResponseWriter, exitStatus chan<- int) {
					// Simulate a long operation that gets cancelled
					select {
					case <-ctx.Done():
						exitStatus <- proxy.StatusClientClosed
					case <-time.After(100 * time.Millisecond):
						exitStatus <- proxy.StatusM3U8Parsed
					}
				}

				return manager
			},
			expectedStatus: http.StatusOK, // The response headers are still sent
			expectedError:  false,
		},
		{
			name: "stream with EOF condition",
			path: "/test.m3u8",
			setupMocks: func() *mockStreamManager {
				manager := &mockStreamManager{}

				manager.loadBalancerFunc = func(ctx context.Context, req *http.Request, session *store.Session) (*http.Response, string, string, string, error) {
					resp := mockResponse(http.StatusOK, "EOF content")
					resp.Header.Set("Content-Type", "application/vnd.apple.mpegurl")
					return resp, "http://eof.com", "1", "1", nil
				}

				manager.proxyStreamFunc = func(ctx context.Context, selectedIndex, selectedSubIndex string, resp *http.Response, r *http.Request, w http.ResponseWriter, exitStatus chan<- int) {
					exitStatus <- proxy.StatusEOF
				}

				return manager
			},
			expectedStatus: http.StatusOK,
			expectedError:  false,
		},
		{
			name: "invalid stream URL with special characters",
			path: "/test@#$.m3u8",
			setupMocks: func() *mockStreamManager {
				return &mockStreamManager{}
			},
			expectedStatus: http.StatusInternalServerError,
			expectedError:  false,
		},
		{
			name: "load balancer network error",
			path: "/test.m3u8",
			setupMocks: func() *mockStreamManager {
				manager := &mockStreamManager{}

				manager.loadBalancerFunc = func(ctx context.Context, req *http.Request, session *store.Session) (*http.Response, string, string, string, error) {
					return nil, "", "", "", errors.New("network connection error")
				}

				return manager
			},
			expectedStatus: http.StatusInternalServerError,
			expectedError:  true,
		},
		{
			name: "stream with server error response",
			path: "/test.m3u8",
			setupMocks: func() *mockStreamManager {
				manager := &mockStreamManager{}

				manager.loadBalancerFunc = func(ctx context.Context, req *http.Request, session *store.Session) (*http.Response, string, string, string, error) {
					return mockResponse(http.StatusInternalServerError, "server error"), "http://error.com", "1", "1", nil
				}

				return manager
			},
			expectedStatus: http.StatusInternalServerError,
			expectedError:  true,
		},
		{
			name: "stream with custom headers",
			path: "/test.m3u8",
			setupMocks: func() *mockStreamManager {
				manager := &mockStreamManager{}

				manager.loadBalancerFunc = func(ctx context.Context, req *http.Request, session *store.Session) (*http.Response, string, string, string, error) {
					resp := mockResponse(http.StatusOK, "content with headers")
					resp.Header.Set("X-Custom-Header", "test-value")
					resp.Header.Set("Content-Type", "application/vnd.apple.mpegurl")
					return resp, "http://headers.com", "1", "1", nil
				}

				manager.proxyStreamFunc = func(ctx context.Context, selectedIndex, selectedSubIndex string, resp *http.Response, r *http.Request, w http.ResponseWriter, exitStatus chan<- int) {
					exitStatus <- proxy.StatusM3U8Parsed
				}

				return manager
			},
			expectedStatus: http.StatusOK,
			expectedError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := tt.setupMocks()
			handler := NewStreamHandler(manager, logger.Default)

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
			case "stream with custom headers":
				if w.Header().Get("X-Custom-Header") != "test-value" {
					t.Error("custom header not properly set")
				}
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
