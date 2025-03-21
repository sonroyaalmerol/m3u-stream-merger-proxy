package main

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m3u-stream-merger/config"
	"m3u-stream-merger/handlers"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/sourceproc"
)

// responseWriterPiper implements http.ResponseWriter and pipes the body to an io.PipeWriter.
type responseWriterPiper struct {
	pw     *io.PipeWriter
	header http.Header
	status int
}

func (rw *responseWriterPiper) Header() http.Header {
	return rw.header
}

func (rw *responseWriterPiper) Write(data []byte) (int, error) {
	if rw.status == 0 {
		rw.WriteHeader(http.StatusOK)
	}
	return rw.pw.Write(data)
}

func (rw *responseWriterPiper) WriteHeader(statusCode int) {
	rw.status = statusCode
}

func TestStreamHTTPHandler(t *testing.T) {
	// Create temp directory for test data
	tempDir, err := os.MkdirTemp("", "m3u-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp directory: %v", err)
	}
	defer func() {
		t.Log("Cleaning up temporary directory:", tempDir)
		//if err := os.RemoveAll(tempDir); err != nil {
		//	t.Errorf("Failed to cleanup temp directory: %v", err)
		//}
	}()

	// Set up test environment
	testDataPath := filepath.Join(tempDir, "data")
	t.Log("Setting up test data path:", testDataPath)
	if err := os.MkdirAll(testDataPath, 0755); err != nil {
		t.Fatalf("Failed to create test data directory: %v", err)
	}

	tempPath := filepath.Join(testDataPath, "temp")
	t.Log("Creating temp directory:", tempPath)
	if err := os.MkdirAll(tempPath, 0755); err != nil {
		t.Fatalf("Failed to create streams directory: %v", err)
	}

	config.SetConfig(&config.Config{
		TempPath: tempPath,
		DataPath: testDataPath,
	})

	// Initialize handlers with test configuration
	t.Log("Initializing handlers with test configuration")
	streamHandler := handlers.NewStreamHTTPHandler(
		handlers.NewDefaultProxyInstance(),
		logger.Default,
	)

	// Set up test environment variables
	m3uURL := "https://gist.githubusercontent.com/sonroyaalmerol/64ce9eddf169366b29bf621b4370ec02/raw/d23e3c43f1961d946c6851a714af2809e21dc3b9/test-m3u.m3u"
	t.Log("Setting M3U_URL_1:", m3uURL)
	t.Setenv("M3U_URL_1", m3uURL)
	t.Setenv("DEBUG", "true")

	// Test M3U playlist generation
	t.Log("Testing M3U playlist generation")
	m3uReq := httptest.NewRequest(http.MethodGet, "/playlist.m3u", nil)
	m3uW := httptest.NewRecorder()

	processor := sourceproc.NewProcessor()
	err = processor.Run(context.Background(), m3uReq)
	if err != nil {
		t.Fatal(err)
	}
	m3uHandler := handlers.NewM3UHTTPHandler(logger.Default, processor.GetResultPath())

	m3uHandler.ServeHTTP(m3uW, m3uReq)

	m3uReq = httptest.NewRequest(http.MethodGet, "/playlist.m3u", nil)
	m3uW = httptest.NewRecorder()
	m3uHandler.ServeHTTP(m3uW, m3uReq)
	if m3uW.Code != http.StatusOK {
		t.Errorf("Playlist Route - Expected status code %d, got %d", http.StatusOK, m3uW.Code)
		t.Log("Response Body:", m3uW.Body.String())
	}

	// Get streams and test each one
	t.Log("Retrieving streams from store")
	t.Logf("Found %d streams", processor.GetCount())
	if processor.GetCount() == 0 {
		t.Error("No streams found in store")
		// Log cache contents for debugging
		if contents, err := os.ReadFile(processor.GetResultPath()); err == nil {
			t.Log("Processed contents:", string(contents))
		} else {
			t.Log("Failed to read cache file:", err)
		}
	}

	// Track if at least one stream passes
	var streamPassed bool

	file, err := os.Open(processor.GetResultPath())
	if err != nil {
		t.Error(err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	for scanner.Scan() {
		success := true // Track success for this stream
		line := scanner.Text()
		if isUrl := strings.HasPrefix(line, "http"); isUrl {
			t.Run(line, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodGet, line, nil)
				pr, pw := io.Pipe()
				defer pr.Close()

				rw := &responseWriterPiper{
					pw:     pw,
					header: make(http.Header),
				}

				// Create a channel to coordinate test completion
				done := make(chan struct{})

				// Start the stream handler in a goroutine
				go func() {
					streamHandler.ServeHTTP(rw, req)
					close(done)
				}()

				// Read and compare streams for a set duration
				testDuration := 2 * time.Second
				timer := time.NewTimer(testDuration)

				buffer2 := make([]byte, 32*1024)

				var totalBytes2 int64

				for {
					select {
					case <-timer.C:
						t.Logf("Test completed after %v", testDuration)
						t.Logf("Total bytes read: %d", totalBytes2)
						if totalBytes2 == 0 {
							t.Logf("No data received from one or both streams")
							success = false
						}
						return
					default:
						// Read from response stream
						n2, err2 := pr.Read(buffer2)
						if err2 != nil && err2 != io.EOF {
							t.Logf("Error reading response stream: %v", err2)
							success = false
							return
						}

						if n2 > 0 {
							totalBytes2 += int64(n2)
						}

						// Verify data is being received
						if n2 > 0 {
							t.Logf("Received data: %d bytes", n2)
						}

						// Small delay to prevent tight loop
						time.Sleep(10 * time.Millisecond)
					}
				}
			})

			if success {
				streamPassed = true
				break // Exit after first successful stream
			}
		}
	}

	if err := scanner.Err(); err != nil {
		t.Error(err)
	}

	// Only fail if no streams passed
	if !streamPassed {
		t.Error("No streams passed the test")
	}
}
