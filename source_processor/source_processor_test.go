package sourceproc

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"m3u-stream-merger/config"
	"m3u-stream-merger/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testDataLock sync.Mutex

func setupTestEnvironment(t *testing.T) func() {
	testDataLock.Lock()
	defer testDataLock.Unlock()

	tempDir, err := os.MkdirTemp("", "m3u-test-*")
	require.NoError(t, err)

	originalConfig := config.GetConfig()

	testConfig := &config.Config{
		DataPath: filepath.Join(tempDir, "data"),
		TempPath: filepath.Join(tempDir, "temp"),
	}
	config.SetConfig(testConfig)

	// First test M3U with news, sports, and movies
	testM3U1 := `#EXTM3U
#EXTINF:-1 tvg-id="cnn.us" tvg-name="CNN US" tvg-logo="http://example.com/cnn.png" tvg-chno="1" group-title="News",CNN US
http://example.com/cnn
#EXTINF:-1 tvg-id="bbc.news" tvg-name="BBC News" tvg-logo="http://example.com/bbc.png" tvg-chno="2" group-title="News",BBC News
http://example.com/bbc
#EXTINF:-1 tvg-id="espn.us" tvg-name="ESPN US" tvg-logo="http://example.com/espn.png" tvg-chno="100" group-title="Sports",ESPN US
http://example.com/espn
#EXTINF:-1 tvg-id="nba.tv" tvg-name="NBA TV" tvg-logo="http://example.com/nba.png" tvg-chno="101" group-title="Sports",NBA TV
http://example.com/nba
#EXTINF:-1 tvg-id="hbo.us" tvg-name="HBO US" tvg-logo="http://example.com/hbo.png" tvg-chno="200" group-title="Movies",HBO US
http://example.com/hbo
#EXTINF:-1 tvg-id="netflix" tvg-name="Netflix" tvg-logo="http://example.com/netflix.png" tvg-chno="201" group-title="Movies",Netflix Movies
http://example.com/netflix
`

	// Second test M3U with entertainment and documentaries
	testM3U2 := `#EXTM3U
#EXTINF:-1 tvg-id="fox.us" tvg-name="FOX US" tvg-logo="http://example.com/fox.png" tvg-chno="300" group-title="Entertainment",FOX US
http://example.com/fox
#EXTINF:-1 tvg-id="nbc.us" tvg-name="NBC US" tvg-logo="http://example.com/nbc.png" tvg-chno="301" group-title="Entertainment",NBC US
http://example.com/nbc
#EXTINF:-1 tvg-id="discovery" tvg-name="Discovery Channel" tvg-logo="http://example.com/discovery.png" tvg-chno="400" group-title="Documentary",Discovery Channel
http://example.com/discovery
#EXTINF:-1 tvg-id="natgeo" tvg-name="National Geographic" tvg-logo="http://example.com/natgeo.png" tvg-chno="401" group-title="Documentary",National Geographic
http://example.com/natgeo
`

	// Third test M3U with kids and music channels
	testM3U3 := `#EXTM3U
#EXTINF:-1 tvg-id="disney" tvg-name="Disney Channel" tvg-logo="http://example.com/disney.png" tvg-chno="500" group-title="Kids",Disney Channel
http://example.com/disney
#EXTINF:-1 tvg-id="nick" tvg-name="Nickelodeon" tvg-logo="http://example.com/nick.png" tvg-chno="501" group-title="Kids",Nickelodeon
http://example.com/nick
#EXTINF:-1 tvg-id="mtv" tvg-name="MTV" tvg-logo="http://example.com/mtv.png" tvg-chno="600" group-title="Music",MTV
http://example.com/mtv
#EXTINF:-1 tvg-id="vh1" tvg-name="VH1" tvg-logo="http://example.com/vh1.png" tvg-chno="601" group-title="Music",VH1
http://example.com/vh1
#EXTINF:-1 tvg-id="vevo" tvg-name="VEVO Hits" tvg-logo="http://example.com/vevo.png" tvg-chno="602" group-title="Music",VEVO Hits
http://example.com/vevo
`

	// Create temp directory
	require.NoError(t, os.MkdirAll(testConfig.TempPath, 0755))

	// Write all three M3U files
	m3uPath1 := filepath.Join(testConfig.TempPath, "test1.m3u")
	m3uPath2 := filepath.Join(testConfig.TempPath, "test2.m3u")
	m3uPath3 := filepath.Join(testConfig.TempPath, "test3.m3u")

	require.NoError(t, os.WriteFile(m3uPath1, []byte(testM3U1), 0644))
	require.NoError(t, os.WriteFile(m3uPath2, []byte(testM3U2), 0644))
	require.NoError(t, os.WriteFile(m3uPath3, []byte(testM3U3), 0644))

	// Set environment variables for all three M3Us
	os.Setenv("M3U_URL_1", fmt.Sprintf("file://%s", m3uPath1))
	os.Setenv("M3U_URL_2", fmt.Sprintf("file://%s", m3uPath2))
	os.Setenv("M3U_URL_3", fmt.Sprintf("file://%s", m3uPath3))

	return func() {
		testDataLock.Lock()
		defer testDataLock.Unlock()

		if cache := M3uCache.cache.Load(); cache != nil {
			cache.processedStreams.clear()
		}
		M3uCache.cache.Store(nil)

		config.SetConfig(originalConfig)
		utils.ResetCaches()

		os.RemoveAll(tempDir)

		os.Unsetenv("M3U_URL_1")
		os.Unsetenv("M3U_URL_2")
		os.Unsetenv("M3U_URL_3")
	}
}

func waitForCache(t *testing.T, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cache := M3uCache.cache.Load()
		if cache != nil && !cache.IsProcessing() {
			// Check if cache file exists and has content
			if _, err := os.Stat(config.GetM3UCachePath()); err == nil {
				content, err := os.ReadFile(config.GetM3UCachePath())
				if err == nil && len(content) > 0 && strings.Contains(string(content), "EXTINF") {
					return
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("Cache did not complete processing within timeout")
}

type testStreamInfo struct {
	group string
	chno  string
	name  string
}

func parseM3UContent(content string) []testStreamInfo {
	var streams []testStreamInfo
	lines := strings.Split(content, "\n")
	var currentStream testStreamInfo

	for _, line := range lines {
		if strings.HasPrefix(line, "#EXTINF") {
			// Extract group-title
			if match := regexp.MustCompile(`group-title="([^"]+)"`).FindStringSubmatch(line); len(match) > 1 {
				currentStream.group = match[1]
			}
			// Extract tvg-chno
			if match := regexp.MustCompile(`tvg-chno="([^"]+)"`).FindStringSubmatch(line); len(match) > 1 {
				currentStream.chno = match[1]
			}
			// Extract name (after the comma)
			if idx := strings.LastIndex(line, ","); idx != -1 {
				currentStream.name = strings.TrimSpace(line[idx+1:])
			}
			streams = append(streams, currentStream)
			currentStream = testStreamInfo{}
		}
	}
	return streams
}

func TestRevalidatingGetM3U(t *testing.T) {
	cleanup := setupTestEnvironment(t)
	defer cleanup()

	tests := []struct {
		name          string
		force         bool
		sortingKey    string
		sortingDir    string
		setup         func(t *testing.T)
		validateOrder func(t *testing.T, streams []testStreamInfo)
	}{
		{
			name:       "initial generation with default sorting",
			force:      false,
			sortingKey: "",
			sortingDir: "asc",
			setup: func(t *testing.T) {
				M3uCache.cache.Store(nil)
			},
			validateOrder: func(t *testing.T, streams []testStreamInfo) {
				// Verify all streams are present
				assert.Equal(t, 15, len(streams), "Should have 15 channels")

				// Verify groups are present
				groups := make(map[string]bool)
				for _, s := range streams {
					groups[s.group] = true
				}
				expectedGroups := []string{"News", "Sports", "Movies", "Entertainment", "Documentary", "Kids", "Music"}
				for _, g := range expectedGroups {
					assert.True(t, groups[g], "Should contain group: %s", g)
				}
			},
		},
		{
			name:       "force regeneration with tvg-chno sorting",
			force:      true,
			sortingKey: "tvg-chno",
			sortingDir: "asc",
			setup: func(t *testing.T) {
				os.Setenv("SORTING_KEY", "tvg-chno")
				os.Setenv("SORTING_DIRECTION", "asc")
			},
			validateOrder: func(t *testing.T, streams []testStreamInfo) {
				// Convert to numbers and verify they're in ascending order
				var numbers []int
				for _, s := range streams {
					num, err := strconv.Atoi(s.chno)
					require.NoError(t, err)
					numbers = append(numbers, num)
				}

				for i := 1; i < len(numbers); i++ {
					assert.GreaterOrEqual(t, numbers[i], numbers[i-1],
						"Channel numbers should be in ascending order, got %d after %d",
						numbers[i], numbers[i-1])
				}

				// Also verify we have all expected numbers
				expectedNumbers := []int{1, 2, 100, 101, 200, 201, 300, 301, 400, 401, 500, 501, 600, 601, 602}
				sort.Ints(numbers)
				assert.Equal(t, expectedNumbers, numbers, "Should have all expected channel numbers in order")
			},
		},
		{
			name:       "group sorting",
			force:      true,
			sortingKey: "tvg-group",
			sortingDir: "asc",
			setup: func(t *testing.T) {
				os.Setenv("SORTING_KEY", "tvg-group")
				os.Setenv("SORTING_DIRECTION", "asc")
			},
			validateOrder: func(t *testing.T, streams []testStreamInfo) {
				// Groups should be in alphabetical order
				lastGroup := ""
				for _, s := range streams {
					assert.GreaterOrEqual(t, strings.Compare(s.group, lastGroup), 0,
						"Groups should be in alphabetical order, got %s after %s", s.group, lastGroup)
					lastGroup = s.group
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup(t)

			// Make request to start processing
			req := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
			RevalidatingGetM3U(req, tt.force)

			// Wait for cache to complete
			waitForCache(t, 5*time.Second)

			// Read final cache file
			content, err := os.ReadFile(config.GetM3UCachePath())
			require.NoError(t, err)
			contentStr := string(content)

			// Basic validation
			assert.Contains(t, contentStr, "#EXTM3U")

			// Parse content and validate order
			streams := parseM3UContent(contentStr)
			tt.validateOrder(t, streams)
		})
	}
}

func TestConcurrentAccess(t *testing.T) {
	cleanup := setupTestEnvironment(t)
	defer cleanup()

	const numGoroutines = 10
	done := make(chan bool, numGoroutines)

	// First request to initialize cache
	req := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
	RevalidatingGetM3U(req, true)

	// Wait for initial cache to complete
	waitForCache(t, 5*time.Second)

	// Read initial content to compare later
	initialContent, err := os.ReadFile(config.GetM3UCachePath())
	require.NoError(t, err)

	// Make concurrent requests
	startTime := time.Now()
	for i := 0; i < numGoroutines; i++ {
		go func() {
			req := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
			result := RevalidatingGetM3U(req, false)
			assert.Contains(t, result, "#EXTM3U")
			assert.Contains(t, result, "CNN US") // Check at least one channel
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < numGoroutines; i++ {
		select {
		case <-done:
			// Success
		case <-time.After(5 * time.Second):
			t.Fatal("Timeout waiting for goroutine")
		}
	}

	// Verify final content matches initial content (no corruption)
	finalContent, err := os.ReadFile(config.GetM3UCachePath())
	require.NoError(t, err)
	assert.Equal(t, string(initialContent), string(finalContent),
		"Cache content should not change during concurrent reads")

	assert.Less(t, time.Since(startTime), 5*time.Second)
}

func TestSortingVariations(t *testing.T) {
	cleanup := setupTestEnvironment(t)
	defer cleanup()

	sortingTests := []struct {
		name      string
		key       string
		direction string
		validate  func(t *testing.T, content string)
	}{
		{
			name:      "sort by name ascending",
			key:       "",
			direction: "asc",
			validate: func(t *testing.T, content string) {
				lastFoundIdx := -1
				channels := []string{"BBC News", "CNN US", "Discovery Channel", "Disney Channel"}
				for _, channel := range channels {
					idx := strings.Index(content, channel)
					assert.Greater(t, idx, lastFoundIdx)
					lastFoundIdx = idx
				}
			},
		},
		{
			name:      "sort by channel number descending",
			key:       "tvg-chno",
			direction: "desc",
			validate: func(t *testing.T, content string) {
				numbers := []string{"602", "601", "600", "501", "500"}
				lastFoundIdx := -1
				for _, num := range numbers {
					idx := strings.Index(content, fmt.Sprintf(`tvg-chno="%s"`, num))
					assert.Greater(t, idx, lastFoundIdx)
					lastFoundIdx = idx
				}
			},
		},
	}

	for _, tt := range sortingTests {
		t.Run(tt.name, func(t *testing.T) {
			// Set sorting environment variables
			os.Setenv("SORTING_KEY", tt.key)
			os.Setenv("SORTING_DIRECTION", tt.direction)

			// Generate new cache
			req := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
			RevalidatingGetM3U(req, true)

			// Wait for cache to complete
			waitForCache(t, 5*time.Second)

			// Read and validate content
			content, err := os.ReadFile(config.GetM3UCachePath())
			require.NoError(t, err)
			tt.validate(t, string(content))
		})
	}
}
