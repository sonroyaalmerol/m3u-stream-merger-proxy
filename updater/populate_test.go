package updater

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"m3u-stream-merger/config"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/xtream"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func populatePanel(fail bool) (http.Handler, *atomic.Int32) {
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/player_api.php", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("action") != "get_series_info" || fail {
			calls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		calls.Add(1)
		if r.URL.Query().Get("series_id") != "300" {
			_, _ = fmt.Fprint(w, `{"info":{"name":"Lazy Show"},"episodes":{}}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"info":{"name":"Lazy Show"},"episodes":{"1":[{"id":301,"episode_num":2,"container_extension":"mkv"}]}}`)
	})
	return mux, &calls
}

func TestPopulateSourceFillsFragment(t *testing.T) {
	panel, calls := populatePanel(false)
	server := httptest.NewServer(panel)
	defer server.Close()

	tempDir := t.TempDir()
	config.SetConfig(&config.Config{DataPath: filepath.Join(tempDir, "data")})
	t.Setenv("XTREAM_URL_1", server.URL)
	t.Setenv("XTREAM_USERNAME_1", "user")
	t.Setenv("XTREAM_PASSWORD_1", "pass")
	t.Setenv("XTREAM_SERIES_BG_DELAY_MS", "50")

	require.NoError(t, os.MkdirAll(config.GetSeriesCacheDirPath(), 0755))
	stubPath := filepath.Join(config.GetSeriesCacheDirPath(), "stubs-1.bin")
	require.NoError(t, xtream.WriteSeriesStubs(stubPath, []xtream.SeriesStub{
		{UpstreamID: 300, Name: "Lazy Show", Group: "Drama"},
	}))

	u := &Updater{logger: logger.Default}
	n := u.populateSource(context.Background(), stubPath, "1")
	assert.Equal(t, 1, n)

	frag := filepath.Join(config.GetSeriesCacheDirPath(), "frag-1.m3u")
	entries, err := xtream.ReadSeriesFragment(frag)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, uint64(300), entries[0].UpstreamID)
	require.Contains(t, entries[0].Lines[0], `tvg-name="Lazy Show S1E2"`)

	start := time.Now()
	n = u.populateSource(context.Background(), stubPath, "1")
	elapsed := time.Since(start)
	assert.Equal(t, 1, n)
	assert.GreaterOrEqual(t, elapsed, 50*time.Millisecond, "rate cap must delay the refetch")
	assert.Equal(t, int32(2), calls.Load())
}

func TestPopulateSourceAbortsOnFailures(t *testing.T) {
	panel, calls := populatePanel(true)
	server := httptest.NewServer(panel)
	defer server.Close()

	tempDir := t.TempDir()
	config.SetConfig(&config.Config{DataPath: filepath.Join(tempDir, "data")})
	t.Setenv("XTREAM_URL_2", server.URL)
	t.Setenv("XTREAM_SERIES_BG_DELAY_MS", "50")

	require.NoError(t, os.MkdirAll(config.GetSeriesCacheDirPath(), 0755))
	stubPath := filepath.Join(config.GetSeriesCacheDirPath(), "stubs-2.bin")
	stubs := make([]xtream.SeriesStub, 50)
	for i := range stubs {
		stubs[i] = xtream.SeriesStub{UpstreamID: uint64(1000 + i), Name: fmt.Sprintf("Show %d", i), Group: "Drama"}
	}
	require.NoError(t, xtream.WriteSeriesStubs(stubPath, stubs))

	populateBackoffMax = 100 * time.Millisecond
	u := &Updater{logger: logger.Default}
	n := u.populateSource(context.Background(), stubPath, "2")
	assert.Equal(t, 0, n)
	assert.Less(t, calls.Load(), int32(len(stubs)), "aborts instead of draining every stub")
}
