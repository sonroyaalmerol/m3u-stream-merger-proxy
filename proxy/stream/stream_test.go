package stream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/proxy/loadbalancer"
	"m3u-stream-merger/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type mockResponseWriter struct {
	written     []byte
	statusCode  int
	headersSent http.Header
	err         error
	mu          sync.Mutex
}

func (m *mockResponseWriter) Header() http.Header {
	if m.headersSent == nil {
		m.headersSent = make(http.Header)
	}
	return m.headersSent
}

func (m *mockResponseWriter) Write(data []byte) (int, error) {
	if m.err != nil {
		return 0, m.err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.written = append(m.written, data...)
	return len(data), nil
}

func (m *mockResponseWriter) WriteHeader(statusCode int) {
	m.statusCode = statusCode
}

func (m *mockResponseWriter) Flush() {
	// Mock implementation
}

type mockHLSServer struct {
	server        *httptest.Server
	mediaPlaylist string
	segments      map[string][]byte
	logger        logger.Logger
}

func newMockHLSServer() *mockHLSServer {
	m := &mockHLSServer{
		segments: make(map[string][]byte),
		logger:   logger.Default,
	}

	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check for request context cancellation
		done := make(chan struct{})
		go func() {
			<-r.Context().Done()
			close(done)
		}()

		select {
		case <-done:
			m.logger.Debug("Request context cancelled")
			return
		default:
			m.handleRequest(w, r)
		}
	}))

	return m
}

func (m *mockHLSServer) handleRequest(w http.ResponseWriter, r *http.Request) {
	m.logger.Debugf("Mock server received request: %s", r.URL.Path)

	switch {
	case strings.HasSuffix(r.URL.Path, ".m3u8"):
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte(m.mediaPlaylist))

	case strings.HasSuffix(r.URL.Path, ".ts"):
		segmentKey := r.URL.Path
		if !strings.HasPrefix(segmentKey, "/") {
			segmentKey = "/" + segmentKey
		}
		if data, ok := m.segments[segmentKey]; ok {
			w.Header().Set("Content-Type", "video/MP2T")
			_, _ = w.Write(data)
		} else {
			m.logger.Errorf("Segment not found: %s", segmentKey)
			w.WriteHeader(http.StatusNotFound)
		}

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (m *mockHLSServer) Close() {
	if m.server != nil {
		m.server.Close()
	}
}

func TestM3U8StreamHandler_HandleHLSStream(t *testing.T) {
	segment1Data := []byte("TESTSEGMNT1!")
	segment2Data := []byte("TESTSEGMNT2!")

	tests := []struct {
		name           string
		config         *StreamConfig
		setupMock      func(*mockHLSServer)
		writeError     error
		expectedResult StreamResult
	}{
		{
			name: "successful media playlist",
			config: &StreamConfig{
				TimeoutSeconds:   5,
				ChunkSize:        1024,
				SharedBufferSize: 5,
			},
			setupMock: func(m *mockHLSServer) {
				m.mediaPlaylist = `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:10
#EXTINF:10.0,
/segment1.ts
#EXTINF:10.0,
/segment2.ts
#EXT-X-ENDLIST`
				m.segments["/segment1.ts"] = segment1Data
				m.segments["/segment2.ts"] = segment2Data
			},
			writeError: nil,
			expectedResult: StreamResult{
				BytesWritten: 24, // Two segments of exactly 12 bytes each
				Error:        io.EOF,
				Status:       proxy.StatusEOF,
			},
		},
		{
			name: "write error during streaming",
			config: &StreamConfig{
				TimeoutSeconds:   5,
				ChunkSize:        1024,
				SharedBufferSize: 5,
			},
			setupMock: func(m *mockHLSServer) {
				m.mediaPlaylist = `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:10
#EXTINF:10.0,
/segment1.ts
#EXT-X-ENDLIST`
				m.segments["/segment1.ts"] = segment1Data
			},
			writeError: errors.New("write error"),
			expectedResult: StreamResult{
				BytesWritten: 0,
				Error:        errors.New("write error"),
				Status:       proxy.StatusClientClosed,
			},
		},
		{
			name: "continuous media playlist without endlist",
			config: &StreamConfig{
				TimeoutSeconds:   2,
				ChunkSize:        1024,
				SharedBufferSize: 5,
			},
			setupMock: func(m *mockHLSServer) {
				m.mediaPlaylist = `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:12
#EXT-X-MEDIA-SEQUENCE:3086
#EXTINF:12.008000,
/segment1.ts
#EXTINF:12.008000,
/segment2.ts`
				m.segments["/segment1.ts"] = segment1Data
				m.segments["/segment2.ts"] = segment2Data
			},
			writeError: nil,
			expectedResult: StreamResult{
				BytesWritten: 24, // First pass through the segments
				Error:        errors.New("stream timeout: no new segments"),
				Status:       proxy.StatusEOF,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a context with timeout that's slightly shorter than the test timeout
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()

			mockServer := newMockHLSServer()
			defer mockServer.Close()

			tt.setupMock(mockServer)

			cm := store.NewConcurrencyManager()
			coordinator := NewStreamCoordinator("test_id", tt.config, cm, logger.Default)

			// Make first request with short timeout
			reqCtx, reqCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer reqCancel()

			req, _ := http.NewRequestWithContext(reqCtx, "GET", mockServer.server.URL+"/playlist.m3u8", nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("Failed to get mock response: %v", err)
			}

			handler := NewM3U8StreamHandler(tt.config, coordinator, logger.Default)
			writer := &mockResponseWriter{err: tt.writeError}
			lbRes := loadbalancer.LoadBalancerResult{Response: resp, Index: "1"}

			// Create a goroutine to handle connection timeout
			resultCh := make(chan StreamResult, 1)
			go func() {
				resultCh <- handler.HandleHLSStream(ctx, &lbRes, writer, "test-addr")
			}()

			// Wait for result or timeout
			var result StreamResult
			select {
			case result = <-resultCh:
			case <-time.After(10000 * time.Millisecond): // Slightly longer than context timeout
				t.Fatal("Test timed out waiting for result")
			}

			if result.Status != tt.expectedResult.Status {
				t.Errorf("HandleHLSStream() status = %v, want %v", result.Status, tt.expectedResult.Status)
			}
			if result.BytesWritten != tt.expectedResult.BytesWritten {
				t.Errorf("HandleHLSStream() bytesWritten = %v, want %v", result.BytesWritten, tt.expectedResult.BytesWritten)
			}
			if tt.expectedResult.Error != nil {
				if result.Error == nil || !strings.Contains(result.Error.Error(), tt.expectedResult.Error.Error()) {
					t.Errorf("HandleHLSStream() error = %v, want error containing %v", result.Error, tt.expectedResult.Error)
				}
			}
		})
	}
}

// Test MediaStreamHandler
func TestMediaStreamHandler_HandleMediaStream(t *testing.T) {
	tests := []struct {
		name           string
		config         *StreamConfig
		responseBody   string
		responseStatus int
		writeError     error
		expectedResult StreamResult
	}{
		{
			name: "successful stream",
			config: &StreamConfig{
				TimeoutSeconds:   5,
				ChunkSize:        1024,
				SharedBufferSize: 5,
			},
			responseBody:   "test content",
			responseStatus: http.StatusOK,
			writeError:     nil,
			expectedResult: StreamResult{
				BytesWritten: 12,
				Error:        io.EOF,
				Status:       proxy.StatusEOF,
			},
		},
		{
			name: "write error",
			config: &StreamConfig{
				TimeoutSeconds:   5,
				ChunkSize:        1024,
				SharedBufferSize: 5,
			},
			responseBody:   "test content",
			responseStatus: http.StatusOK,
			writeError:     errors.New("write error"),
			expectedResult: StreamResult{
				BytesWritten: 0,
				Error:        errors.New("write error"),
				Status:       0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			config := NewDefaultStreamConfig()
			cm := store.NewConcurrencyManager()
			coordinator := NewStreamCoordinator("test_id", config, cm, logger.Default)
			handler := NewMediaStreamHandler(tt.config, coordinator, logger.Default)

			resp := &http.Response{
				StatusCode: tt.responseStatus,
				Body:       io.NopCloser(strings.NewReader(tt.responseBody)),
				Header:     make(http.Header),
			}
			resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(tt.responseBody)))

			writer := &mockResponseWriter{err: tt.writeError}
			lbRes := loadbalancer.LoadBalancerResult{Response: resp, Index: "1"}

			result := handler.HandleMediaStream(ctx, &lbRes, writer, "test-addr")

			if result.Status != tt.expectedResult.Status {
				t.Errorf("HandleMediaStream() status = %v, want %v", result.Status, tt.expectedResult.Status)
			}
			if result.BytesWritten != tt.expectedResult.BytesWritten {
				t.Errorf("HandleMediaStream() bytesWritten = %v, want %v", result.BytesWritten, tt.expectedResult.BytesWritten)
			}
			if (result.Error != nil) != (tt.expectedResult.Error != nil) {
				t.Errorf("HandleMediaStream() error = %v, want %v", result.Error, tt.expectedResult.Error)
			}
		})
	}
}

func TestStreamInstance_ProxyStream(t *testing.T) {
	segment1Data := []byte("TESTSEGMNT1!")
	segment2Data := []byte("TESTSEGMNT2!")

	tests := []struct {
		name           string
		method         string
		contentType    string
		setupMock      func(*mockHLSServer)
		expectedStatus int
	}{
		{
			name:        "handle m3u8 stream",
			method:      http.MethodGet,
			contentType: "application/vnd.apple.mpegurl",
			setupMock: func(m *mockHLSServer) {
				m.mediaPlaylist = fmt.Sprintf(`#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:10
#EXTINF:10.0,
%s/segment1.ts
#EXTINF:10.0,
%s/segment2.ts
#EXT-X-ENDLIST`, m.server.URL, m.server.URL)

				m.segments["/segment1.ts"] = segment1Data
				m.segments["/segment2.ts"] = segment2Data
			},
			expectedStatus: proxy.StatusEOF,
		},
		{
			name:        "handle media stream",
			method:      http.MethodGet,
			contentType: "video/MP2T",
			setupMock: func(m *mockHLSServer) {
				m.segments["/media"] = []byte("media content!")
			},
			expectedStatus: proxy.StatusEOF,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockServer := newMockHLSServer()
			defer mockServer.Close()

			tt.setupMock(mockServer)

			config := NewDefaultStreamConfig()
			cm := store.NewConcurrencyManager()
			coordinator := NewStreamCoordinator("test_id", config, cm, logger.Default)

			instance, err := NewStreamInstance(cm, config,
				WithLogger(logger.Default))
			if err != nil {
				t.Fatalf("Failed to create StreamInstance: %v", err)
			}

			path := "/playlist.m3u8"
			if tt.contentType == "video/MP2T" {
				path = "/media"
			}

			req, _ := http.NewRequest(tt.method, mockServer.server.URL+path, nil)
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Request:    req,
			}
			resp.Header.Set("Content-Type", tt.contentType)

			if tt.contentType == "video/MP2T" {
				resp.Body = io.NopCloser(bytes.NewReader(mockServer.segments["/media"]))
			} else {
				resp, err = http.Get(mockServer.server.URL + path)
				if err != nil {
					t.Fatalf("Failed to get mock response: %v", err)
				}
			}

			writer := &mockResponseWriter{}
			statusChan := make(chan int, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			lbRes := loadbalancer.LoadBalancerResult{Response: resp, Index: "1"}
			instance.ProxyStream(ctx, coordinator, &lbRes, req, writer, statusChan)

			select {
			case status := <-statusChan:
				if status != tt.expectedStatus {
					t.Errorf("ProxyStream() status = %v, want %v", status, tt.expectedStatus)
				}
			case <-time.After(5 * time.Second):
				t.Error("ProxyStream() timed out waiting for status")
			}
		})
	}
}
