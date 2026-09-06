package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"m3u-stream-merger/config"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/xtream"

	"github.com/cespare/xxhash"
	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const xtreamTestM3U = `#EXTM3U
#EXTINF:-1 tvg-id="cnn.id" tvg-name="CNN" tvg-type="live" tvg-group="News" tvg-logo="http://img/cnn.png",CNN
http://base:8080/p/live/u/p/SLUG_LIVE.ts
#EXTINF:-1 tvg-type="movie" tvg-group="Movies",Cool Movie
http://base:8080/p/movie/u/p/SLUG_MOVIE.mp4
#EXTINF:-1 tvg-type="series" tvg-group="Drama",Test Show S01E02
http://base:8080/p/series/u/p/SLUG_EP2.mkv
`

func setupXtreamHandler(t *testing.T) *XtreamHTTPHandler {
	t.Helper()

	tempDir := t.TempDir()
	config.SetConfig(&config.Config{
		DataPath: filepath.Join(tempDir, "data"),
		TempPath: filepath.Join(tempDir, "temp"),
	})

	m3uPath := filepath.Join(tempDir, "merged.m3u")
	require.NoError(t, os.WriteFile(m3uPath, []byte(xtreamTestM3U), 0644))
	require.NoError(t, xtream.GetCatalog().Rebuild(m3uPath))

	t.Cleanup(func() {
		_ = os.Unsetenv("CREDENTIALS")
	})

	_ = os.Setenv("CREDENTIALS", "")

	return NewXtreamHTTPHandler(
		NewStreamHTTPHandler(NewDefaultProxyInstance(), logger.Default),
		logger.Default,
	)
}

func playerAPIRequest(t *testing.T, h *XtreamHTTPHandler, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/player_api.php?"+query, nil)
	rec := httptest.NewRecorder()
	h.ServePlayerAPI(rec, req)
	return rec
}

func TestXtreamRootResponse(t *testing.T) {
	h := setupXtreamHandler(t)

	rec := playerAPIRequest(t, h, "username=u&password=p")
	require.Equal(t, http.StatusOK, rec.Code)

	var root xtream.RootResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &root))
	assert.Equal(t, 1, root.UserInfo.Auth)
	assert.Equal(t, "Active", root.UserInfo.Status)
	assert.Equal(t, "u", root.UserInfo.Username)
	assert.NotEmpty(t, root.ServerInfo.URL)
}

func TestXtreamAuth(t *testing.T) {
	h := setupXtreamHandler(t)
	t.Setenv("CREDENTIALS", "u:p")

	rec := playerAPIRequest(t, h, "username=u&password=wrong")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	rec = playerAPIRequest(t, h, "username=u&password=p")
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestXtreamLiveStreams(t *testing.T) {
	h := setupXtreamHandler(t)

	rec := playerAPIRequest(t, h, "action=get_live_streams")
	require.Equal(t, http.StatusOK, rec.Code)

	var streams []xtream.RawLiveStream
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &streams))
	require.Len(t, streams, 1)
	assert.Equal(t, "CNN", streams[0].Name)
	assert.Equal(t, "cnn.id", streams[0].EPGChannelID)
	assert.Equal(t, strconv.FormatUint(xxhash.Sum64String("CNN"), 10), streams[0].StreamID.String())
}

func TestXtreamCategoriesAndFilter(t *testing.T) {
	h := setupXtreamHandler(t)

	rec := playerAPIRequest(t, h, "action=get_live_categories")
	require.Equal(t, http.StatusOK, rec.Code)

	var cats []xtream.RawCategory
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &cats))
	require.Len(t, cats, 1)
	assert.Equal(t, "News", cats[0].CategoryName)

	newsID := cats[0].CategoryID.String()
	rec = playerAPIRequest(t, h, "action=get_live_streams&category_id="+newsID)
	var streams []xtream.RawLiveStream
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &streams))
	assert.Len(t, streams, 1)

	rec = playerAPIRequest(t, h, "action=get_live_streams&category_id=99999")
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &streams))
	assert.Empty(t, streams)
}

func TestXtreamSeriesInfo(t *testing.T) {
	h := setupXtreamHandler(t)

	seriesID := xxhash.Sum64String("series|Test Show")
	rec := playerAPIRequest(t, h, "action=get_series_info&series_id="+strconv.FormatUint(seriesID, 10))
	require.Equal(t, http.StatusOK, rec.Code)

	var info xtream.RawSeriesInfo
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &info))
	assert.Equal(t, "Test Show", info.Info.Name)
	require.Len(t, info.Episodes["1"], 1)
	assert.Equal(t, "Test Show S01E02", info.Episodes["1"][0].Title)
	assert.Equal(t, "mkv", info.Episodes["1"][0].ContainerExtension)
}

func TestXtreamGetPHP(t *testing.T) {
	h := setupXtreamHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/get.php?username=u&password=p&type=m3u_plus", nil)
	rec := httptest.NewRecorder()
	h.ServeGetPHP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, "http://example.com/live/u/p/")
	assert.Contains(t, body, "http://example.com/movie/u/p/")
	assert.Contains(t, body, fmt.Sprintf("http://example.com/movie/u/p/%d.mp4", xxhash.Sum64String("Cool Movie")))
	assert.Contains(t, body, fmt.Sprintf("http://example.com/series/u/p/%d.mkv", xxhash.Sum64String("Test Show S01E02")))
}

func TestXtreamServeStreamUnknownID(t *testing.T) {
	h := setupXtreamHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/live/u/p/999999.ts", nil)
	rec := httptest.NewRecorder()
	h.ServeStream(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	req = httptest.NewRequest(http.MethodGet, "/live/u/p/notanid.ts", nil)
	rec = httptest.NewRecorder()
	h.ServeStream(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestXtreamServeStreamAuth(t *testing.T) {
	h := setupXtreamHandler(t)
	t.Setenv("CREDENTIALS", "u:p")

	req := httptest.NewRequest(http.MethodGet, "/live/wrong/p/1.ts", nil)
	rec := httptest.NewRecorder()
	h.ServeStream(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestXtreamShortEPG(t *testing.T) {
	h := setupXtreamHandler(t)

	require.NoError(t, os.MkdirAll(config.GetEPGDirPath(), 0755))
	epgXML := `<?xml version="1.0" encoding="UTF-8"?>
<tv>
  <programme start="20240101120000 +0000" stop="20240101130000 +0000" channel="cnn.id">
    <title>News Hour</title>
    <desc>Latest news</desc>
  </programme>
</tv>`
	require.NoError(t, os.WriteFile(config.GetEPGPath(), []byte(epgXML), 0644))

	cnnID := xxhash.Sum64String("CNN")
	rec := playerAPIRequest(t, h, "action=get_short_epg&stream_id="+strconv.FormatUint(cnnID, 10))
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	assert.True(t, strings.Contains(body, "2024-01-01 12:00:00"), "start time not formatted: %s", body)
}
