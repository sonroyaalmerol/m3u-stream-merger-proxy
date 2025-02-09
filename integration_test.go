package main

import (
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
	"m3u-stream-merger/store"
	"m3u-stream-merger/utils"
)

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
	m3uHandler := handlers.NewM3UHTTPHandler(logger.Default)
	cachePath := config.GetM3UCachePath()

	streamHandler := handlers.NewStreamHTTPHandler(
		handlers.NewDefaultProxyInstance(),
		logger.Default,
	)

	// Set up test environment variables
	m3uURL := "https://gist.githubusercontent.com/sonroyaalmerol/de1c90e8681af040924da5d15c7f530d/raw/06844df09e69ea278060252ca5aa8d767eb4543d/test-m3u.m3u"
	t.Log("Setting M3U_URL_1:", m3uURL)
	t.Setenv("M3U_URL_1", m3uURL)
	t.Setenv("DEBUG", "true")

	// Download M3U source
	t.Log("Downloading M3U source")
	if err := store.DownloadM3USource("1"); err != nil {
		t.Fatalf("Failed to download M3U source: %v", err)
	}

	// Test M3U playlist generation
	t.Log("Testing M3U playlist generation")
	m3uReq := httptest.NewRequest(http.MethodGet, "/playlist.m3u", nil)
	m3uW := httptest.NewRecorder()
	m3uHandler.ServeHTTP(m3uW, m3uReq)

	if m3uW.Code != http.StatusOK {
		t.Errorf("Playlist Route - Expected status code %d, got %d", http.StatusOK, m3uW.Code)
		t.Log("Response Body:", m3uW.Body.String())
	}

	// Get streams and test each one
	t.Log("Retrieving streams from store")
	streams := store.GetCurrentStreams()
	t.Logf("Found %d streams", len(streams))
	if len(streams) == 0 {
		t.Error("No streams found in store")
		// Log cache contents for debugging
		if cacheContents, err := os.ReadFile(cachePath); err == nil {
			t.Log("Cache contents:", string(cacheContents))
		} else {
			t.Log("Failed to read cache file:", err)
		}
	}

	// Track if at least one stream passes
	var streamPassed bool

	for _, stream := range streams {
		success := true // Track success for this stream
		t.Run(stream.Title, func(t *testing.T) {
			t.Logf("Testing stream: %s", stream.Title)
			t.Logf("Stream URLs: %v", stream.URLs)

			genStreamUrl := strings.TrimSpace(store.GenerateStreamURL("", stream))
			t.Logf("Generated stream URL: %s", genStreamUrl)

			req := httptest.NewRequest(http.MethodGet, genStreamUrl, nil)
			w := httptest.NewRecorder()

			// Create a channel to coordinate test completion
			done := make(chan struct{})

			// Start the stream handler in a goroutine
			go func() {
				streamHandler.ServeHTTP(w, req)
				close(done)
			}()

			// Fetch original stream for comparison
			originalURL := stream.URLs["1"]["0"]
			t.Logf("Fetching original stream from: %s", originalURL)

			res, err := utils.HTTPClient.Get(originalURL)
			if err != nil {
				t.Logf("Failed to fetch original stream: %v", err)
				success = false
				return
			}
			defer res.Body.Close()

			// Read and compare streams for a set duration
			testDuration := 2 * time.Second
			timer := time.NewTimer(testDuration)

			buffer1 := make([]byte, 32*1024)
			buffer2 := make([]byte, 32*1024)

			var totalBytes1, totalBytes2 int64

			for {
				select {
				case <-timer.C:
					t.Logf("Test completed after %v", testDuration)
					t.Logf("Total bytes read - Original: %d, Response: %d", totalBytes1, totalBytes2)
					if totalBytes1 == 0 || totalBytes2 == 0 {
						t.Logf("No data received from one or both streams")
						success = false
					}
					return
				default:
					// Read from original stream
					n1, err1 := res.Body.Read(buffer1)
					if err1 != nil && err1 != io.EOF {
						t.Logf("Error reading original stream: %v", err1)
						success = false
						return
					}

					// Read from response stream
					n2, err2 := w.Body.Read(buffer2)
					if err2 != nil && err2 != io.EOF {
						t.Logf("Error reading response stream: %v", err2)
						success = false
						return
					}

					// Update totals
					if n1 > 0 {
						totalBytes1 += int64(n1)
					}
					if n2 > 0 {
						totalBytes2 += int64(n2)
					}

					// Verify data is being received
					if n1 > 0 || n2 > 0 {
						t.Logf("Received data - Original: %d bytes, Response: %d bytes", n1, n2)
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

	// Only fail if no streams passed
	if !streamPassed {
		t.Error("No streams passed the test")
	}
}
