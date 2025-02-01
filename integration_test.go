package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m3u-stream-merger/config"
	"m3u-stream-merger/handlers"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/store"
	"m3u-stream-merger/utils"
)

func TestStreamHandler(t *testing.T) {
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
	m3uHandler := handlers.NewM3UHandler(logger.Default)
	cachePath := config.GetM3UCachePath()

	streamHandler := handlers.NewStreamHandler(
		handlers.NewDefaultStreamManager(),
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

	for _, stream := range streams {
		t.Run(stream.Title, func(t *testing.T) {
			t.Logf("Testing stream: %s", stream.Title)
			t.Logf("Stream URLs: %v", stream.URLs)

			genStreamUrl := strings.TrimSpace(store.GenerateStreamURL("", stream))
			t.Logf("Generated stream URL: %s", genStreamUrl)

			req := httptest.NewRequest(http.MethodGet, genStreamUrl, nil)
			w := httptest.NewRecorder()
			t.Log("Sending request to stream handler")
			streamHandler.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("Expected status code %d, got %d", http.StatusOK, w.Code)
				t.Log("Response Headers:", w.Header())
				t.Log("Response Body:", w.Body.String())
				return
			}

			// Fetch original stream for comparison
			originalURL := stream.URLs["1"]["0"]
			t.Logf("Fetching original stream from: %s", originalURL)

			res, err := utils.HTTPClient.Get(originalURL)
			if err != nil {
				t.Errorf("Failed to fetch original stream: %v", err)
				return
			}
			defer res.Body.Close()

			expected, err := io.ReadAll(res.Body)
			if err != nil {
				t.Errorf("Failed to read original stream: %v", err)
				return
			}
			t.Logf("Original stream size: %d bytes", len(expected))

			body, err := io.ReadAll(w.Body)
			if err != nil {
				t.Errorf("Failed to read response body: %v", err)
				return
			}
			t.Logf("Response stream size: %d bytes", len(body))

			if !bytes.Equal(body, expected) {
				t.Error("Stream content mismatch")
				if len(body) < 100 && len(expected) < 100 {
					t.Logf("Expected: %v", expected)
					t.Logf("Got: %v", body)
				} else {
					t.Logf("Content length mismatch - Expected: %d, Got: %d", len(expected), len(body))
				}
			} else {
				t.Log("Stream content matches successfully")
			}
		})
	}
}
