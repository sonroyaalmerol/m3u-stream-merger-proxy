package handlers

import (
	"context"
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
	"m3u-stream-merger/sourceproc"
	"m3u-stream-merger/utils"
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
	utils.ResetCaches()

	m3uPath := filepath.Join(tempDir, "merged.m3u")
	require.NoError(t, os.WriteFile(m3uPath, []byte(xtreamTestM3U), 0644))
	t.Setenv("M3U_URL_1", "file://"+m3uPath)
	t.Setenv("BASE_URL", "http://example.com")

	require.NoError(t, sourceproc.NewProcessor().Run(context.Background(), nil))

	t.Setenv("CREDENTIALS", "")

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

	// Panels answer bad credentials with HTTP 200 and auth 0 so players show a credential error.
	rec := playerAPIRequest(t, h, "username=u&password=wrong")
	require.Equal(t, http.StatusOK, rec.Code)
	var root xtream.RootResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &root))
	assert.Equal(t, 0, root.UserInfo.Auth)
	assert.Equal(t, "Disabled", root.UserInfo.Status)

	rec = playerAPIRequest(t, h, "username=u&password=p")
	require.Equal(t, http.StatusOK, rec.Code)
	var ok xtream.RootResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ok))
	assert.Equal(t, 1, ok.UserInfo.Auth)
	assert.Contains(t, ok.UserInfo.AllowedOutputFormats, "m3u8")
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

	var info xtream.SeriesInfoOut
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &info))
	assert.Equal(t, "Test Show", info.Info.Name)
	require.Len(t, info.Episodes["1"], 1)
	assert.Equal(t, "Test Show S01E02", info.Episodes["1"][0].Title)
	assert.Equal(t, "mkv", info.Episodes["1"][0].ContainerExtension)
	assert.Equal(t, 1, info.Episodes["1"][0].Info.Season)
	require.Len(t, info.Seasons, 1)
	assert.Equal(t, 1, info.Seasons[0].SeasonNumber)
	assert.Equal(t, 1, info.Seasons[0].EpisodeCount)
}

func TestXtreamVodInfo(t *testing.T) {
	h := setupXtreamHandler(t)

	vodID := xxhash.Sum64String("Cool Movie")
	rec := playerAPIRequest(t, h, "action=get_vod_info&vod_id="+strconv.FormatUint(vodID, 10))
	require.Equal(t, http.StatusOK, rec.Code)

	var info xtream.VodInfoOut
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &info))
	assert.Equal(t, "Cool Movie", info.Info.Name)
	assert.Equal(t, "Cool Movie", info.MovieData.Name)
	assert.Equal(t, "mp4", info.MovieData.ContainerExtension)
	assert.Equal(t, strconv.FormatUint(vodID, 10), info.MovieData.StreamID.String())
}

func TestXtreamPanelAPI(t *testing.T) {
	h := setupXtreamHandler(t)

	rec := playerAPIRequest(t, h, "username=u&password=p&action=x")
	require.Equal(t, http.StatusOK, rec.Code)

	req := httptest.NewRequest(http.MethodGet, "/panel_api.php?username=u&password=p", nil)
	panel := httptest.NewRecorder()
	h.ServePanelAPI(panel, req)
	require.Equal(t, http.StatusOK, panel.Code)

	var resp xtream.PanelResponse
	require.NoError(t, json.Unmarshal(panel.Body.Bytes(), &resp))
	assert.Equal(t, 1, resp.UserInfo.Auth)
	require.Len(t, resp.Categories[xtream.TypeLive], 1)
	require.Len(t, resp.Categories[xtream.TypeMovie], 1)
	assert.NotEmpty(t, resp.AvailableChannels)
}

func TestXtreamGetPHP(t *testing.T) {
	h := setupXtreamHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/get.php?username=u&password=p&type=m3u_plus", nil)
	rec := httptest.NewRecorder()
	h.ServeGetPHP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, "x-tvg-url=")
	assert.Contains(t, body, "http://example.com/live/u/p/")
	assert.Contains(t, body, "http://example.com/movie/u/p/")
	assert.Contains(t, body, fmt.Sprintf("http://example.com/movie/u/p/%d.mp4", xxhash.Sum64String("Cool Movie")))
	assert.Contains(t, body, fmt.Sprintf("http://example.com/series/u/p/%d.mkv", xxhash.Sum64String("Test Show S01E02")))
}

func TestXtreamGetPHPOutputFormat(t *testing.T) {
	h := setupXtreamHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/get.php?username=u&password=p&output=m3u8", nil)
	rec := httptest.NewRecorder()
	h.ServeGetPHP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), ".m3u8\n")
}

func TestXtreamPlayerAPIPostForm(t *testing.T) {
	h := setupXtreamHandler(t)
	t.Setenv("CREDENTIALS", "u:p")

	form := "username=u&password=p"
	req := httptest.NewRequest(http.MethodPost, "/player_api.php", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServePlayerAPI(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var root xtream.RootResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &root))
	assert.Equal(t, 1, root.UserInfo.Auth)
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

	var short map[string][]xtream.EPGListingOut
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &short))
	require.Len(t, short["epg_listings"], 1)
	listing := short["epg_listings"][0]
	assert.Equal(t, "2024-01-01 12:00:00", listing.Start)
	assert.Equal(t, int64(1704110400), listing.StartTimestamp)
	assert.Equal(t, "cnn.id", listing.ChannelID)
	assert.NotEmpty(t, listing.Title)

	rec = playerAPIRequest(t, h, "action=get_simple_data_table&stream_id="+strconv.FormatUint(cnnID, 10))
	require.Equal(t, http.StatusOK, rec.Code)
	var table map[string][]xtream.EPGListingOut
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &table))
	require.Len(t, table["epg_listings"], 1)
	assert.Equal(t, 1, table["epg_listings"][0].NowPlaying)
}
