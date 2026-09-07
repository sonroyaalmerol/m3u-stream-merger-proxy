package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"m3u-stream-merger/config"
	"m3u-stream-merger/sourceproc"
	"m3u-stream-merger/xtream"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lazyPanel() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/player_api.php", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("username") != "user" || r.URL.Query().Get("password") != "pass" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Query().Get("action") {
		case "get_series_info":
			_, _ = fmt.Fprint(w, `{"info":{"name":"Lazy Show"},"episodes":{"1":[{"id":301,"episode_num":2,"title":"Episode 2","container_extension":"mkv","movie_image":"http://img/ep.png"}]}}`)
		default:
			_, _ = fmt.Fprint(w, `[]`)
		}
	})
	return mux
}

func writeLazyStub(t *testing.T, stub xtream.SeriesStub, idx string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(config.GetSeriesCacheDirPath(), 0755))
	require.NoError(t, xtream.WriteSeriesStubs(
		filepath.Join(config.GetSeriesCacheDirPath(), "stubs-"+idx+".bin"),
		[]xtream.SeriesStub{stub},
	))
}

func TestLazySeriesFlow(t *testing.T) {
	h := setupXtreamHandler(t)
	panel := httptest.NewServer(lazyPanel())
	defer panel.Close()
	t.Setenv("XTREAM_URL_1", panel.URL)
	t.Setenv("XTREAM_USERNAME_1", "user")
	t.Setenv("XTREAM_PASSWORD_1", "pass")

	stub := xtream.SeriesStub{UpstreamID: 300, Name: "Lazy Show", Group: "Drama", Cover: "http://img/show.png"}
	writeLazyStub(t, stub, "1")
	lazyID := sourceproc.SeriesIDFor("Lazy Show")

	rec := playerAPIRequest(t, h, "action=get_series")
	var series []xtream.SeriesOut
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &series))
	names := map[string]bool{}
	for _, s := range series {
		names[s.Name] = true
	}
	assert.True(t, names["Test Show"], "catalog series present")
	assert.True(t, names["Lazy Show"], "stub series listed")
	for _, s := range series {
		if s.Name == "Lazy Show" {
			assert.Equal(t, strconv.FormatUint(lazyID, 10), s.SeriesID.String())
		}
	}

	rec = playerAPIRequest(t, h, "action=get_series_info&series_id="+strconv.FormatUint(lazyID, 10))
	var info xtream.SeriesInfoOut
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &info))
	require.Len(t, info.Episodes["1"], 1)
	epID := sourceproc.StreamIDFor(xtream.EpisodeTitle("Lazy Show", "1", 2))
	assert.Equal(t, strconv.FormatUint(epID, 10), info.Episodes["1"][0].ID)

	frag := filepath.Join(config.GetSeriesCacheDirPath(), "frag-1.m3u")
	entries, err := xtream.ReadSeriesFragment(frag)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, uint64(300), entries[0].UpstreamID)
	assert.Contains(t, entries[0].Lines[0], `tvg-name="Lazy Show S1E2"`)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/series/u/p/%d.mkv", epID), nil)
	rr := httptest.NewRecorder()
	h.ServeStream(rr, req)
	require.Equal(t, http.StatusFound, rr.Code)
	assert.Equal(t, panel.URL+"/series/user/pass/301.mkv", rr.Header().Get("Location"))
}

func TestLazySeriesUnknownID(t *testing.T) {
	h := setupXtreamHandler(t)
	rec := playerAPIRequest(t, h, "action=get_series_info&series_id=1234567890")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"episodes":{}`)
}

func TestServeStreamLazyCacheEviction(t *testing.T) {
	h := setupXtreamHandler(t)
	epID := sourceproc.StreamIDFor(xtream.EpisodeTitle("Gone", "1", 1))
	h.lazyMu.Lock()
	if h.lazyEpisodes == nil {
		h.lazyEpisodes = make(map[uint64]lazyEpisode)
	}
	h.lazyEpisodes[epID] = lazyEpisode{srcIdx: "9", upstreamID: 42, ext: "mkv"}
	h.lazyMu.Unlock()

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/series/u/p/%d.mkv", epID), nil)
	rr := httptest.NewRecorder()
	h.ServeStream(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestStubRegistryMergesSources(t *testing.T) {
	_ = setupXtreamHandler(t)
	writeLazyStub(t, xtream.SeriesStub{UpstreamID: 300, Name: "Shared Show", Group: "Drama"}, "1")
	writeLazyStub(t, xtream.SeriesStub{UpstreamID: 999, Name: "Shared Show", Group: "Drama"}, "2")

	r := &seriesStubRegistry{}
	byID := r.get()
	require.Len(t, byID, 1)
	for _, s := range byID {
		require.Equal(t, sourceproc.SeriesIDFor("Shared Show"), s.SeriesID)
		require.Len(t, s.Sources, 2)
		assert.Equal(t, uint64(300), s.Sources[0].UpstreamID)
		assert.Equal(t, uint64(999), s.Sources[1].UpstreamID)
	}
}

func TestLazySeriesContextCancellation(t *testing.T) {
	h := setupXtreamHandler(t)
	_, cancel := context.WithCancel(context.Background())
	cancel()
	stubs := h.stubs()
	_, ok := stubs[sourceproc.SeriesIDFor("Nobody")]
	assert.False(t, ok)
}
