package stream

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/store"
	"m3u-stream-merger/utils"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type mockResponseWriter struct {
	written     []byte
	statusCode  int
	headersSent http.Header
	err         error
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
	m.written = append(m.written, data...)
	return len(data), nil
}

func (m *mockResponseWriter) WriteHeader(statusCode int) {
	m.statusCode = statusCode
}

// Test M3U8Processor
func TestM3U8Processor_ProcessM3U8Stream(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		baseURL  string
		wantErr  bool
		expected string
	}{
		{
			name:     "process absolute URLs",
			input:    "#EXTM3U\nhttp://example.com/stream.ts",
			baseURL:  "http://base.com/",
			wantErr:  false,
			expected: "#EXTM3U\nhttp://example.com/stream.ts\n",
		},
		{
			name:     "handle newline variations",
			input:    "#EXTM3U\nstream1.ts\nstream2.ts\n",
			baseURL:  "http://base.com/",
			wantErr:  false,
			expected: "#EXTM3U\nhttp://base.com/stream1.ts\nhttp://base.com/stream2.ts\n",
		},
		{
			name:     "process relative URLs",
			input:    "#EXTM3U\n/stream.ts",
			baseURL:  "http://base.com/path/",
			wantErr:  false,
			expected: "#EXTM3U\nhttp://base.com/stream.ts\n",
		},
		{
			name:     "handle empty lines",
			input:    "#EXTM3U\nhttp://example.com/stream.ts\n",
			baseURL:  "http://base.com/",
			wantErr:  false,
			expected: "#EXTM3U\nhttp://example.com/stream.ts\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			processor := NewM3U8Processor(logger.Default)
			writer := &mockResponseWriter{}
			baseURL, _ := url.Parse(tt.baseURL)
			reader := bufio.NewScanner(strings.NewReader(tt.input))

			err := processor.ProcessM3U8Stream(reader, writer, baseURL)

			if (err != nil) != tt.wantErr {
				t.Errorf("ProcessM3U8Stream() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && string(writer.written) != tt.expected {
				t.Errorf("ProcessM3U8Stream() got = %v, want %v", string(writer.written), tt.expected)
			}
		})
	}
}

// Test StreamHandler
func TestStreamHandler_HandleStream(t *testing.T) {
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
			// Create context with timeout
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			config := NewDefaultStreamConfig()
			cm := store.NewConcurrencyManager()
			coordinator := NewStreamCoordinator("test_id", config, cm, logger.Default)
			handler := NewStreamHandler(tt.config, coordinator, logger.Default)

			// Create response with headers to indicate expected EOF
			resp := &http.Response{
				StatusCode: tt.responseStatus,
				Body:       io.NopCloser(strings.NewReader(tt.responseBody)),
				Header:     make(http.Header),
			}
			resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(tt.responseBody)))

			writer := &mockResponseWriter{err: tt.writeError}

			// Run handler with context
			result := handler.HandleStream(ctx, "1", resp, writer, "test-addr")

			// Verify results
			if result.Status != tt.expectedResult.Status {
				t.Errorf("HandleStream() status = %v, want %v", result.Status, tt.expectedResult.Status)
			}
			if result.BytesWritten != tt.expectedResult.BytesWritten {
				t.Errorf("HandleStream() bytesWritten = %v, want %v", result.BytesWritten, tt.expectedResult.BytesWritten)
			}
			if (result.Error != nil) != (tt.expectedResult.Error != nil) {
				t.Errorf("HandleStream() error = %v, want %v", result.Error, tt.expectedResult.Error)
			}
		})
	}
}

// Test StreamInstance
func TestStreamInstance_ProxyStream(t *testing.T) {
	tests := []struct {
		name           string
		method         string
		contentType    string
		responseBody   string
		expectedStatus int
	}{
		{
			name:           "handle m3u8 stream",
			method:         http.MethodGet,
			contentType:    "application/vnd.apple.mpegurl",
			responseBody:   "#EXTM3U\nhttp://example.com/stream.ts",
			expectedStatus: proxy.StatusM3U8Parsed,
		},
		{
			name:           "handle media stream",
			method:         http.MethodGet,
			contentType:    "video/MP2T",
			responseBody:   "media content",
			expectedStatus: proxy.StatusEOF,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := NewDefaultStreamConfig()
			cm := store.NewConcurrencyManager()

			coordinator := NewStreamCoordinator("test_id", config, cm, logger.Default)

			instance, err := NewStreamInstance(cm, config,
				WithLogger(logger.Default))
			if err != nil {
				t.Fatalf("Failed to create StreamInstance: %v", err)
			}

			req, _ := http.NewRequest(tt.method, "http://example.com", nil)
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(tt.responseBody)),
				Request:    req,
			}
			resp.Header.Set("Content-Type", tt.contentType)

			writer := &mockResponseWriter{}
			statusChan := make(chan int, 1)
			ctx := context.Background()

			instance.ProxyStream(ctx, coordinator, "1", resp, req, writer, statusChan)

			status := <-statusChan
			if status != tt.expectedStatus {
				t.Errorf("ProxyStream() status = %v, want %v", status, tt.expectedStatus)
			}
		})
	}
}

// Test IsEOFExpected
func TestIsEOFExpected(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		contentLen  int64
		expectEOF   bool
	}{
		{
			name:        "m3u8 content",
			contentType: "application/vnd.apple.mpegurl",
			contentLen:  -1,
			expectEOF:   true,
		},
		{
			name:        "media content with length",
			contentType: "application/x-mpegurl",
			contentLen:  1000,
			expectEOF:   true,
		},
		{
			name:        "media content without length",
			contentType: "video/MP2T",
			contentLen:  -1,
			expectEOF:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{
				Header:        make(http.Header),
				ContentLength: tt.contentLen,
			}
			resp.Header.Set("Content-Type", tt.contentType)

			result := utils.EOFIsExpected(resp)
			if result != tt.expectEOF {
				t.Errorf("IsEOFExpected() = %v, want %v", result, tt.expectEOF)
			}
		})
	}
}
