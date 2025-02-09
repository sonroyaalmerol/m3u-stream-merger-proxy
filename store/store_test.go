package store

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"m3u-stream-merger/config"
	"m3u-stream-merger/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test helpers
func setupTestEnvironment(t *testing.T) (cleanup func()) {
	tempDir, err := os.MkdirTemp("", "m3u-test-*")
	require.NoError(t, err)

	// Store original config
	originalConfig := config.GetConfig()

	// Create test config with temp directories
	testConfig := &config.Config{
		DataPath: filepath.Join(tempDir, "data"),
		TempPath: filepath.Join(tempDir, "temp"),
	}
	config.SetConfig(testConfig)

	// Create necessary directories
	require.NoError(t, os.MkdirAll(config.GetSourcesDirPath(), 0755))
	require.NoError(t, os.MkdirAll(config.GetStreamsDirPath(), 0755))
	require.NoError(t, os.MkdirAll(filepath.Dir(config.GetM3UCachePath()), 0755))

	// Create test M3U file
	testM3U := `#EXTM3U
#EXTINF:-1 tvg-id="test.1" tvg-name="Test Channel 1" tvg-logo="http://example.com/logo1.png" group-title="News",Test Channel 1
http://example.com/stream1
#EXTINF:-1 tvg-id="test.2" tvg-name="Test Channel 2" tvg-logo="http://example.com/logo2.png" group-title="Sports",Test Channel 2
http://example.com/stream2
`
	m3uPath := filepath.Join(config.GetConfig().TempPath, "test1.m3u")
	err = os.WriteFile(m3uPath, []byte(testM3U), 0644)
	require.NoError(t, err)

	// Set test environment variables
	os.Setenv("M3U_URL_1", fmt.Sprintf("file://%s", m3uPath))
	err = DownloadM3USource("1")
	require.NoError(t, err)

	return func() {
		// Restore original config
		config.SetConfig(originalConfig)

		utils.ResetCaches()

		// Clean up test directories
		os.RemoveAll(tempDir)
	}
}

func TestRevalidatingGetM3U(t *testing.T) {
	cleanup := setupTestEnvironment(t)
	defer cleanup()

	tests := []struct {
		name     string
		force    bool
		setup    func(t *testing.T)
		validate func(t *testing.T, result string)
	}{
		{
			name:  "initial generation",
			force: false,
			setup: func(t *testing.T) {
				ClearCache()
			},
			validate: func(t *testing.T, result string) {
				assert.Contains(t, result, "#EXTM3U")
				assert.Contains(t, result, "Test Channel 1")
				assert.Contains(t, result, "Test Channel 2")
			},
		},
		{
			name:  "force regeneration",
			force: true,
			setup: func(t *testing.T) {
				err := os.WriteFile(config.GetM3UCachePath(), []byte("#EXTM3U\nOLD CONTENT"), 0644)
				require.NoError(t, err)
			},
			validate: func(t *testing.T, result string) {
				assert.Contains(t, result, "Test Channel 1")
				assert.NotContains(t, result, "OLD CONTENT")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup(t)
			req := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
			result := RevalidatingGetM3U(req, tt.force)
			tt.validate(t, result)
		})
	}
}

func TestFiltering(t *testing.T) {
	cleanup := setupTestEnvironment(t)
	defer cleanup()

	tests := []struct {
		name           string
		stream         *StreamInfo
		env            map[string]string
		expectedResult bool
		description    string
	}{
		{
			name: "no filters",
			stream: &StreamInfo{
				Title: "Test Channel",
				Group: "News",
			},
			env:            map[string]string{},
			expectedResult: true,
			description:    "When no filters are set, all streams should pass",
		},
		{
			name: "matching include group",
			stream: &StreamInfo{
				Title: "Test Channel",
				Group: "News",
			},
			env: map[string]string{
				"INCLUDE_GROUPS_1": "News",
				"INCLUDE_GROUPS_2": "Sports",
			},
			expectedResult: true,
			description:    "Stream should pass when its group matches include filter",
		},
		{
			name: "non-matching include group",
			stream: &StreamInfo{
				Title: "Test Channel",
				Group: "Movies",
			},
			env: map[string]string{
				"INCLUDE_GROUPS_1": "News",
				"INCLUDE_GROUPS_2": "Sports",
			},
			expectedResult: false,
			description:    "Stream should be blocked when its group doesn't match include filter",
		},
		{
			name: "matching exclude group only",
			stream: &StreamInfo{
				Title: "Test Channel",
				Group: "News",
			},
			env: map[string]string{
				"EXCLUDE_GROUPS_1": "News",
				"EXCLUDE_GROUPS_2": "Sports",
			},
			expectedResult: false,
			description:    "Stream should be blocked when only exclude filter matches",
		},
		{
			name: "include and exclude both match",
			stream: &StreamInfo{
				Title: "Sports News",
				Group: "News",
			},
			env: map[string]string{
				"INCLUDE_GROUPS_1": "News",
				"EXCLUDE_GROUPS_1": "News",
			},
			expectedResult: true,
			description:    "Include filters should take precedence over exclude filters",
		},
		{
			name: "multiple include filters",
			stream: &StreamInfo{
				Title: "Test Channel",
				Group: "News",
			},
			env: map[string]string{
				"INCLUDE_GROUPS_1": "Movies",
				"INCLUDE_GROUPS_2": "News",
				"INCLUDE_TITLE_1":  "Other",
			},
			expectedResult: true,
			description:    "Stream should pass if it matches any include filter",
		},
		{
			name: "multiple exclude filters",
			stream: &StreamInfo{
				Title: "Test Channel",
				Group: "Movies",
			},
			env: map[string]string{
				"EXCLUDE_GROUPS_1": "News",
				"EXCLUDE_GROUPS_2": "Movies",
			},
			expectedResult: false,
			description:    "Stream should be blocked if it matches any exclude filter",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear environment and filters
			os.Clearenv()
			filtersInitialized = false
			utils.ResetCaches()

			// Set environment variables for the test
			for k, v := range tt.env {
				os.Setenv(k, v)
			}

			result := checkFilter(tt.stream)
			assert.Equal(t, tt.expectedResult, result, tt.description)
		})
	}

	os.Clearenv()
	filtersInitialized = false
	utils.ResetCaches()
}

func TestStreamSorting(t *testing.T) {
	cleanup := setupTestEnvironment(t)
	defer cleanup()

	// Test both sorting and source-based ordering
	streams := map[string]*StreamInfo{
		"c": {Title: "Channel C", TvgChNo: "3", Group: "News", SourceM3U: "m3u_2", SourceIndex: 0},
		"a": {Title: "Channel A", TvgChNo: "1", Group: "Sports", SourceM3U: "m3u_1", SourceIndex: 1},
		"b": {Title: "Channel B", TvgChNo: "2", Group: "Movies", SourceM3U: "m3u_1", SourceIndex: 0},
		"d": {Title: "Channel D", TvgChNo: "4", Group: "Sports", SourceM3U: "m3u_2", SourceIndex: 1},
		"e": {Title: "Channel E", TvgChNo: "5", Group: "News", SourceM3U: "m3u_1", SourceIndex: 2},
	}

	tests := []struct {
		name          string
		sortingKey    string
		sortingDir    string
		expectedOrder []string
	}{
		{
			name:          "sort by title asc",
			sortingKey:    "",
			sortingDir:    "asc",
			expectedOrder: []string{"a", "b", "c", "d", "e"}, // First m3u_1 in original order, then m3u_2
		},
		{
			name:          "sort by tvg-chno desc",
			sortingKey:    "tvg-chno",
			sortingDir:    "desc",
			expectedOrder: []string{"e", "d", "c", "b", "a"},
		},
		{
			name:          "sort by group asc",
			sortingKey:    "tvg-group",
			sortingDir:    "asc",
			expectedOrder: []string{"b", "c", "e", "a", "d"}, // Movies, News, Sports
		},
		{
			name:          "source-based ordering",
			sortingKey:    "source",
			sortingDir:    "",
			expectedOrder: []string{"b", "a", "e", "c", "d"}, // m3u_1 entries first, then m3u_2, each in original order
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("SORTING_KEY", tt.sortingKey)
			os.Setenv("SORTING_DIRECTION", tt.sortingDir)
			result := sortStreams(streams)
			assert.Equal(t, tt.expectedOrder, result,
				"Failed %s: got %v, want %v",
				tt.name,
				result,
				tt.expectedOrder,
			)
		})
	}
}

func TestM3UScanner(t *testing.T) {
	cleanup := setupTestEnvironment(t)
	defer cleanup()

	var scannedStreams []*StreamInfo
	err := M3UScanner("1", "test-session", func(stream *StreamInfo) {
		if stream != nil {
			scannedStreams = append(scannedStreams, stream)
		}
	})

	require.NoError(t, err)
	require.NotEmpty(t, scannedStreams, "Expected scanned streams to not be empty")

	if len(scannedStreams) > 0 {
		assert.Equal(t, "Test Channel 1", scannedStreams[0].Title)
		assert.Equal(t, "News", scannedStreams[0].Group)
	}
}

func TestCacheOperations(t *testing.T) {
	cleanup := setupTestEnvironment(t)
	defer cleanup()

	t.Run("cache write and read", func(t *testing.T) {
		content := "#EXTM3U\nTest Content"
		err := writeCacheToFile(content)
		require.NoError(t, err)

		readContent := readCacheFromFile()
		assert.Equal(t, content, readContent)
	})

	t.Run("clear cache", func(t *testing.T) {
		ClearCache()
		_, err := os.Stat(config.GetM3UCachePath())
		assert.True(t, os.IsNotExist(err))
	})
}

func TestConcurrentAccess(t *testing.T) {
	cleanup := setupTestEnvironment(t)
	defer cleanup()

	const numGoroutines = 5
	done := make(chan bool)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			req := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
			RevalidatingGetM3U(req, false)
			done <- true
		}()
	}

	for i := 0; i < numGoroutines; i++ {
		select {
		case <-done:
			// Success
		case <-time.After(5 * time.Second):
			t.Fatal("Timeout waiting for goroutine")
		}
	}
}
