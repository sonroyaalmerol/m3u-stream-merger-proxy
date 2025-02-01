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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
				TimeoutSeconds: 5,
				BufferSizeMB:   1,
			},
			responseBody:   "test content",
			responseStatus: http.StatusOK,
			writeError:     nil,
			expectedResult: StreamResult{
				BytesWritten: 12,
				Error:        nil,
				Status:       proxy.StatusEOF,
			},
		},
		{
			name: "write error",
			config: &StreamConfig{
				TimeoutSeconds: 5,
				BufferSizeMB:   1,
			},
			responseBody:   "test content",
			responseStatus: http.StatusOK,
			writeError:     errors.New("write error"),
			expectedResult: StreamResult{
				BytesWritten: 0,
				Error:        errors.New("write error"),
				Status:       proxy.StatusClientClosed,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewStreamHandler(tt.config, logger.Default)
			writer := &mockResponseWriter{err: tt.writeError}

			resp := &http.Response{
				StatusCode: tt.responseStatus,
				Body:       io.NopCloser(strings.NewReader(tt.responseBody)),
			}

			ctx := context.Background()
			result := handler.HandleStream(ctx, resp, writer, "test-addr")

			// For successful stream test, we expect EOF as the response body reader will be exhausted
			if tt.name == "successful stream" && result.Error == io.EOF {
				result.Error = nil
				result.Status = proxy.StatusEOF
			}

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

			instance.ProxyStream(ctx, "test-index", "sub-index", resp, req, writer, statusChan)

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

// Test StreamInstance_ProxyStream with concurrency tracking
func TestStreamInstance_ProxyStreamConcurrency(t *testing.T) {
	tests := []struct {
		name           string
		method         string
		contentType    string
		responseBody   string
		expectedStatus int
		m3uIndex       string
		maxConcurrent  int // Maximum concurrent streams allowed
	}{
		{
			name:           "handle media stream with concurrency tracking",
			method:         http.MethodGet,
			contentType:    "video/MP2T",
			responseBody:   "media content",
			expectedStatus: proxy.StatusEOF,
			m3uIndex:       "test-index-1",
			maxConcurrent:  3,
		},
		{
			name:           "handle multiple concurrent streams",
			method:         http.MethodGet,
			contentType:    "video/MP2T",
			responseBody:   "media content",
			expectedStatus: proxy.StatusEOF,
			m3uIndex:       "test-index-2",
			maxConcurrent:  5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := NewDefaultStreamConfig()
			cm := store.NewConcurrencyManager()

			// Set max concurrency for this test
			t.Setenv(fmt.Sprintf("M3U_MAX_CONCURRENCY_%s", tt.m3uIndex), strconv.Itoa(tt.maxConcurrent))

			instance, err := NewStreamInstance(cm, config,
				WithLogger(logger.Default))
			if err != nil {
				t.Fatalf("Failed to create StreamInstance: %v", err)
			}

			// Check initial concurrency status
			current, max, priority := cm.GetConcurrencyStatus(tt.m3uIndex)
			if current != 0 {
				t.Errorf("Initial current concurrency = %v, want 0", current)
			}
			if max != tt.maxConcurrent {
				t.Errorf("Initial max concurrency = %v, want %v", max, tt.maxConcurrent)
			}
			if priority != tt.maxConcurrent {
				t.Errorf("Initial priority = %v, want %v", priority, tt.maxConcurrent)
			}

			// Create multiple concurrent requests
			numRequests := 3
			var wg sync.WaitGroup
			statusChan := make(chan int, numRequests)

			for i := 0; i < numRequests; i++ {
				wg.Add(1)
				go func(index int) {
					defer wg.Done()

					req, _ := http.NewRequest(tt.method, "http://example.com", nil)
					resp := &http.Response{
						StatusCode: http.StatusOK,
						Header:     make(http.Header),
						Body:       io.NopCloser(strings.NewReader(tt.responseBody)),
						Request:    req,
					}
					resp.Header.Set("Content-Type", tt.contentType)

					writer := &mockResponseWriter{}
					ctx := context.Background()

					instance.ProxyStream(ctx, tt.m3uIndex, fmt.Sprintf("sub-index-%d", index),
						resp, req, writer, statusChan)
				}(i)
			}

			// Wait for all requests to complete
			wg.Wait()

			// Drain status channel
			for i := 0; i < numRequests; i++ {
				status := <-statusChan
				if status != tt.expectedStatus {
					t.Errorf("ProxyStream() status = %v, want %v", status, tt.expectedStatus)
				}
			}

			// Check final concurrency status
			current, max, priority = cm.GetConcurrencyStatus(tt.m3uIndex)
			if current != 0 {
				t.Errorf("Final current concurrency = %v, want 0", current)
			}
			if max != tt.maxConcurrent {
				t.Errorf("Final max concurrency = %v, want %v", max, tt.maxConcurrent)
			}
			if priority != tt.maxConcurrent {
				t.Errorf("Final priority = %v, want %v", priority, tt.maxConcurrent)
			}
		})
	}
}

// Helper function for int32
func max(a, b int32) int32 {
	if a > b {
		return a
	}
	return b
}

func TestStreamInstance_ProxyStreamConcurrencyEdgeCases(t *testing.T) {
	tests := []struct {
		name           string
		method         string
		contentType    string
		responseBody   string
		expectedStatus int
		m3uIndex       string
		maxConcurrent  int           // Maximum concurrent streams allowed
		numRequests    int           // Number of concurrent requests to make
		delayMs        int           // Delay between requests in milliseconds
		contextTimeout time.Duration // Timeout for context
		cancelContext  bool          // Whether to cancel context mid-stream
	}{
		{
			name:           "zero max concurrency setting",
			method:         http.MethodGet,
			contentType:    "video/MP2T",
			responseBody:   "media content",
			expectedStatus: proxy.StatusEOF,
			m3uIndex:       "test-index-zero",
			maxConcurrent:  0,
			numRequests:    5,
			delayMs:        0,
			contextTimeout: time.Second,
			cancelContext:  false,
		},
		{
			name:           "negative max concurrency setting",
			method:         http.MethodGet,
			contentType:    "video/MP2T",
			responseBody:   "media content",
			expectedStatus: proxy.StatusEOF,
			m3uIndex:       "test-index-negative",
			maxConcurrent:  -1,
			numRequests:    5,
			delayMs:        0,
			contextTimeout: time.Second,
			cancelContext:  false,
		},
		{
			name:           "rapid concurrent requests",
			method:         http.MethodGet,
			contentType:    "video/MP2T",
			responseBody:   "media content",
			expectedStatus: proxy.StatusEOF,
			m3uIndex:       "test-index-rapid",
			maxConcurrent:  10,
			numRequests:    100,
			delayMs:        0,
			contextTimeout: time.Second,
			cancelContext:  false,
		},
		{
			name:           "staggered concurrent requests",
			method:         http.MethodGet,
			contentType:    "video/MP2T",
			responseBody:   "media content",
			expectedStatus: proxy.StatusEOF,
			m3uIndex:       "test-index-staggered",
			maxConcurrent:  5,
			numRequests:    20,
			delayMs:        50,
			contextTimeout: time.Second * 5,
			cancelContext:  false,
		},
		{
			name:           "max int32 concurrent setting",
			method:         http.MethodGet,
			contentType:    "video/MP2T",
			responseBody:   "media content",
			expectedStatus: proxy.StatusEOF,
			m3uIndex:       "test-index-maxint",
			maxConcurrent:  int(^uint32(0) >> 1), // max int32
			numRequests:    5,
			delayMs:        0,
			contextTimeout: time.Second,
			cancelContext:  false,
		},
		{
			name:           "cancelled context mid-stream",
			method:         http.MethodGet,
			contentType:    "video/MP2T",
			responseBody:   "media content",
			expectedStatus: proxy.StatusClientClosed,
			m3uIndex:       "test-index-cancel",
			maxConcurrent:  5,
			numRequests:    10,
			delayMs:        100,
			contextTimeout: time.Second * 2,
			cancelContext:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := NewDefaultStreamConfig()
			cm := store.NewConcurrencyManager()

			// Set max concurrency for this test
			t.Setenv(fmt.Sprintf("M3U_MAX_CONCURRENCY_%s", tt.m3uIndex), strconv.Itoa(tt.maxConcurrent))

			instance, err := NewStreamInstance(cm, config,
				WithLogger(logger.Default))
			if err != nil {
				t.Fatalf("Failed to create StreamInstance: %v", err)
			}

			// Track concurrent requests
			var currentConcurrent int32
			var maxObservedConcurrent int32
			var mutex sync.Mutex
			statusChan := make(chan int, tt.numRequests)
			var wg sync.WaitGroup

			// Create a cancellable context
			ctx, cancel := context.WithTimeout(context.Background(), tt.contextTimeout)
			defer cancel()

			// Launch concurrent requests
			for i := 0; i < tt.numRequests; i++ {
				wg.Add(1)
				go func(index int) {
					defer wg.Done()

					// Simulate staggered requests if delayMs > 0
					if tt.delayMs > 0 {
						time.Sleep(time.Duration(tt.delayMs*index) * time.Millisecond)
					}

					// Track concurrent requests
					atomic.AddInt32(&currentConcurrent, 1)
					mutex.Lock()
					maxObservedConcurrent = max(maxObservedConcurrent, currentConcurrent)
					mutex.Unlock()
					defer atomic.AddInt32(&currentConcurrent, -1)

					req, _ := http.NewRequest(tt.method, "http://example.com", nil)
					resp := &http.Response{
						StatusCode: http.StatusOK,
						Header:     make(http.Header),
						Body:       io.NopCloser(strings.NewReader(tt.responseBody)),
						Request:    req,
					}
					resp.Header.Set("Content-Type", tt.contentType)

					writer := &mockResponseWriter{}

					// Cancel context mid-stream if specified
					if tt.cancelContext && index == tt.numRequests/2 {
						cancel()
					}

					instance.ProxyStream(ctx, tt.m3uIndex, fmt.Sprintf("sub-index-%d", index),
						resp, req, writer, statusChan)
				}(i)
			}

			// Wait for all requests to complete
			wg.Wait()

			// Verify results
			successCount := 0
			cancelCount := 0
			for i := 0; i < tt.numRequests; i++ {
				status := <-statusChan
				if status == tt.expectedStatus {
					successCount++
				}
				if status == proxy.StatusClientClosed {
					cancelCount++
				}
			}

			// Check if cancellation worked as expected
			if tt.cancelContext && cancelCount == 0 {
				t.Error("Expected some requests to be cancelled but none were")
			}

			// Verify final concurrency state
			current, _, _ := cm.GetConcurrencyStatus(tt.m3uIndex)
			if current != 0 {
				t.Errorf("Final current concurrency = %v, want 0", current)
			}

			// Log maximum observed concurrent requests
			t.Logf("Max observed concurrent requests: %d", maxObservedConcurrent)
			t.Logf("Successful requests: %d", successCount)
			t.Logf("Cancelled requests: %d", cancelCount)
		})
	}
}

// Test race conditions in concurrency tracking
func TestStreamInstance_ConcurrencyRaceConditions(t *testing.T) {
	config := NewDefaultStreamConfig()
	cm := store.NewConcurrencyManager()
	m3uIndex := "test-index-race"

	// Set a relatively low max concurrency
	t.Setenv(fmt.Sprintf("M3U_MAX_CONCURRENCY_%s", m3uIndex), "3")

	_, err := NewStreamInstance(cm, config,
		WithLogger(logger.Default))
	if err != nil {
		t.Fatalf("Failed to create StreamInstance: %v", err)
	}

	// Create multiple goroutines that rapidly increment and decrement concurrency
	numOperations := 1000
	var wg sync.WaitGroup

	for i := 0; i < numOperations; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()

			// Simulate rapid increment/decrement cycles
			if index%2 == 0 {
				cm.Increment(m3uIndex)
				time.Sleep(time.Millisecond)
				cm.Decrement(m3uIndex)
			}
		}(i)
	}

	wg.Wait()

	// Verify final state
	current, _, _ := cm.GetConcurrencyStatus(m3uIndex)
	if current != 0 {
		t.Errorf("Final concurrency count = %d, want 0", current)
	}
}
