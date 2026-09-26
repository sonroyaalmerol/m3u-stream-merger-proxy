package handlers

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"m3u-stream-merger/config"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/sourceproc"
	"m3u-stream-merger/utils"
	"m3u-stream-merger/xtream"

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
	return setupXtreamHandlerFrom(t, xtreamTestM3U)
}

func setupXtreamHandlerFrom(t *testing.T, m3u string) *XtreamHTTPHandler {
	t.Helper()

	tempDir := t.TempDir()
	config.SetConfig(&config.Config{
		DataPath: filepath.Join(tempDir, "data"),
		TempPath: filepath.Join(tempDir, "temp"),
	})

	m3uPath := filepath.Join(tempDir, "merged.m3u")
	require.NoError(t, os.WriteFile(m3uPath, []byte(m3u), 0644))
	t.Setenv("M3U_URL_1", "file://"+m3uPath)
	t.Setenv("BASE_URL", "http://example.com")
	utils.ResetCaches()
	t.Cleanup(utils.ResetCaches)

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
	assert.Equal(t, strconv.FormatUint(sourceproc.StreamIDFor("CNN"), 10), streams[0].StreamID.String())
}

func TestXtreamCategoriesAndFilter(t *testing.T) {
	h := setupXtreamHandler(t)

	rec := playerAPIRequest(t, h, "action=get_live_categories")
	require.Equal(t, http.StatusOK, rec.Code)

	var cats []xtream.RawCategory
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &cats))
	require.Len(t, cats, 1)
	assert.Equal(t, "News", cats[0].CategoryName)

	newsID := cats[0].CategoryID
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

	seriesID := sourceproc.SeriesIDFor("Test Show")
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

// TestXtreamWireTypes pins string-vs-number JSON typing to what real panels emit.
func TestXtreamWireTypes(t *testing.T) {
	h := setupXtreamHandler(t)

	decodeFirst := func(query string) map[string]any {
		rec := playerAPIRequest(t, h, query)
		require.Equal(t, http.StatusOK, rec.Code)
		var rows []map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
		require.NotEmpty(t, rows)
		return rows[0]
	}

	cat := decodeFirst("action=get_live_categories")
	assert.IsType(t, "", cat["category_id"])
	assert.IsType(t, float64(0), cat["parent_id"])

	live := decodeFirst("action=get_live_streams")
	assert.IsType(t, "", live["category_id"])
	assert.IsType(t, float64(0), live["stream_id"])
	assert.IsType(t, []any{}, live["category_ids"])

	show := decodeFirst("action=get_series")
	assert.IsType(t, "", show["category_id"])
	assert.IsType(t, float64(0), show["series_id"])
	assert.Equal(t, xtream.TypeSeries, show["stream_type"])

	rec := playerAPIRequest(t, h, "action=get_series_info&series_id="+strconv.FormatUint(sourceproc.SeriesIDFor("Test Show"), 10))
	var info map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &info))
	episodes := info["episodes"].(map[string]any)["1"].([]any)
	require.NotEmpty(t, episodes)
	episode := episodes[0].(map[string]any)
	assert.IsType(t, "", episode["id"])
	assert.IsType(t, "", episode["episode_num"])
	assert.IsType(t, float64(0), episode["season"])

	rec = playerAPIRequest(t, h, "action=get_profile")
	var profile xtream.RootResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &profile))
	assert.Equal(t, 1, profile.UserInfo.Auth)
	assert.NotEmpty(t, profile.ServerInfo.URL)
}

func TestXtreamVodInfo(t *testing.T) {
	h := setupXtreamHandler(t)

	vodID := sourceproc.StreamIDFor("Cool Movie")
	rec := playerAPIRequest(t, h, "action=get_vod_info&vod_id="+strconv.FormatUint(vodID, 10))
	require.Equal(t, http.StatusOK, rec.Code)

	var info xtream.VodInfoOut
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &info))
	assert.Equal(t, "Cool Movie", info.Info.Name)
	assert.Equal(t, "Cool Movie", info.MovieData.Name)
	assert.Equal(t, "mp4", info.MovieData.ContainerExtension)
	assert.Equal(t, strconv.FormatUint(vodID, 10), info.MovieData.StreamID.String())
}

// TiviMate parses stream_id/series_id/category_id as a signed 32-bit int and drops anything wider.
func TestXtreamIDsFitInt32(t *testing.T) {
	h := setupXtreamHandler(t)

	for _, action := range []string{"get_live_streams", "get_vod_streams", "get_series", "get_live_categories"} {
		rec := playerAPIRequest(t, h, "username=u&password=p&action="+action)
		require.Equal(t, http.StatusOK, rec.Code)

		var rows []map[string]any
		dec := json.NewDecoder(rec.Body)
		dec.UseNumber()
		require.NoError(t, dec.Decode(&rows), action)
		require.NotEmpty(t, rows, action)
		for _, row := range rows {
			for _, field := range []string{"stream_id", "series_id", "category_id"} {
				raw, ok := row[field]
				if !ok {
					continue
				}
				id, err := strconv.ParseInt(strings.Trim(fmt.Sprint(raw), `"`), 10, 32)
				require.NoError(t, err, "%s.%s=%v exceeds int32", action, field, raw)
				assert.Positive(t, id, "%s.%s", action, field)
			}
		}
	}
}

func TestXtreamUserInfoWireTypes(t *testing.T) {
	h := setupXtreamHandler(t)

	rec := playerAPIRequest(t, h, "username=u&password=p")
	var root map[string]map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &root))

	user := root["user_info"]
	assert.Nil(t, user["exp_date"])
	assert.Equal(t, "0", user["active_cons"])
	assert.Equal(t, "0", user["is_trial"])
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

func TestXtreamGzipJSON(t *testing.T) {
	h := setupXtreamHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/player_api.php?username=u&password=p&action=get_live_streams", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServePlayerAPI(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "gzip", rec.Header().Get("Content-Encoding"))

	gz, err := gzip.NewReader(rec.Body)
	require.NoError(t, err)
	body, err := io.ReadAll(gz)
	require.NoError(t, err)
	var streams []xtream.RawLiveStream
	require.NoError(t, json.Unmarshal(body, &streams))
	require.Len(t, streams, 1)
}

func TestXtreamGetPHPPlainType(t *testing.T) {
	h := setupXtreamHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/get.php?username=u&password=p&type=m3u", nil)
	rec := httptest.NewRecorder()
	h.ServeGetPHP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "#EXTINF:-1,CNN")
	assert.NotContains(t, body, "tvg-id=")
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
	assert.Contains(t, body, fmt.Sprintf("http://example.com/movie/u/p/%d.mp4", sourceproc.StreamIDFor("Cool Movie")))
	assert.Contains(t, body, fmt.Sprintf("http://example.com/series/u/p/%d.mkv", sourceproc.StreamIDFor("Test Show S01E02")))
}

func TestXtreamVODByteRange(t *testing.T) {
	var gotRange string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRange = r.Header.Get("Range")
		w.Header().Set("Content-Range", "bytes 5-9/10")
		w.Header().Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("56789"))
	}))
	defer upstream.Close()

	m3u := fmt.Sprintf("#EXTM3U\n#EXTINF:-1 tvg-type=%q tvg-group=%q,Cool Movie\n%s/movie.mp4\n",
		"movie", "Movies", upstream.URL)
	h := setupXtreamHandlerFrom(t, m3u)

	id := sourceproc.StreamIDFor("Cool Movie")
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/movie/u/p/%d.mp4", id), nil)
	req.Header.Set("Range", "bytes=5-9")
	rec := httptest.NewRecorder()
	h.ServeStream(rec, req)

	require.Equal(t, http.StatusPartialContent, rec.Code)
	assert.Equal(t, "bytes=5-9", gotRange)
	assert.Equal(t, "bytes 5-9/10", rec.Header().Get("Content-Range"))
	assert.Equal(t, "56789", rec.Body.String())
}

func TestXtreamGetPHPOutputFormat(t *testing.T) {
	h := setupXtreamHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/get.php?username=u&password=p&output=m3u8", nil)
	rec := httptest.NewRecorder()
	h.ServeGetPHP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), ".m3u8\n")
}

func TestXtreamCatchup(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "video/mp2t")
		_, _ = w.Write([]byte("archived stream"))
	}))
	defer upstream.Close()

	m3u := fmt.Sprintf("#EXTM3U\n#EXTINF:-1 tvg-id=%q tvg-name=%q tvg-type=%q catchup=%q catchup-days=%q,CNN\n%s/live/provider/secret/100.ts\n#EXTINF:-1 tvg-id=%q tvg-name=%q tvg-type=%q,CNN\nhttp://127.0.0.1:1/live/other/secret/100.ts\n",
		"cnn.id", "CNN", "live", "xtream", "7", upstream.URL, "cnn.id", "CNN", "live")
	h := setupXtreamHandlerFrom(t, m3u)

	rec := playerAPIRequest(t, h, "action=get_live_streams")
	var streams []xtream.LiveStreamOut
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &streams))
	require.Len(t, streams, 1)
	assert.Equal(t, 1, streams[0].TVArchive)
	assert.Equal(t, 7, streams[0].TVArchiveDuration)

	id := sourceproc.StreamIDFor("CNN")
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/timeshift/u/p/90/2026-09-25:12-30/%d.ts", id), nil)
	rec = httptest.NewRecorder()
	h.ServeCatchup(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "/timeshift/provider/secret/90/2026-09-25:12-30/100.ts", gotPath)
	assert.Equal(t, "archived stream", rec.Body.String())

	for _, path := range []string{
		fmt.Sprintf("/timeshift/u/p/0/2026-09-25:12-30/%d.ts", id),
		fmt.Sprintf("/timeshift/u/p/90/not-a-date/%d.ts", id),
	} {
		rec = httptest.NewRecorder()
		h.ServeCatchup(rec, httptest.NewRequest(http.MethodGet, path, nil))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	}

	rec = httptest.NewRecorder()
	path := fmt.Sprintf("/timeshift/u/p/10081/2026-09-25:12-30/%d.ts", id)
	h.ServeCatchup(rec, httptest.NewRequest(http.MethodGet, path, nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
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
	airing := time.Now().UTC()
	epgXML := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<tv>
  <programme start="20240101120000 +0000" stop="20240101130000 +0000" channel="cnn.id">
    <title>News Hour</title>
    <desc>Latest news</desc>
  </programme>
  <programme start="%s +0000" stop="%s +0000" channel="cnn.id">
    <title>On Air Now</title>
    <desc>Currently airing</desc>
  </programme>
</tv>`,
		airing.Add(-time.Hour).Format("20060102150405"),
		airing.Add(time.Hour).Format("20060102150405"))
	require.NoError(t, os.WriteFile(config.GetEPGPath(), []byte(epgXML), 0644))

	cnnID := sourceproc.StreamIDFor("CNN")
	rec := playerAPIRequest(t, h, "action=get_short_epg&stream_id="+strconv.FormatUint(cnnID, 10))
	require.Equal(t, http.StatusOK, rec.Code)

	var short map[string][]xtream.EPGListingOut
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &short))
	require.Len(t, short["epg_listings"], 1)
	listing := short["epg_listings"][0]
	assert.Equal(t, "cnn.id", listing.ChannelID)
	assert.NotEmpty(t, listing.Title)
	assert.Equal(t, 1, listing.NowPlaying)
	assert.NotEmpty(t, listing.StartTimestamp)

	rec = playerAPIRequest(t, h, "action=get_simple_data_table&stream_id="+strconv.FormatUint(cnnID, 10))
	require.Equal(t, http.StatusOK, rec.Code)
	var table map[string][]xtream.EPGListingOut
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &table))
	require.Len(t, table["epg_listings"], 1)
	assert.Equal(t, 1, table["epg_listings"][0].NowPlaying)
}

func TestXtreamCatchupEPG(t *testing.T) {
	m3u := `#EXTM3U
#EXTINF:-1 tvg-id="cnn.id" tvg-name="CNN" tvg-type="live" catchup="xtream" catchup-days="7",CNN
http://panel/live/u/p/100.ts
`
	h := setupXtreamHandlerFrom(t, m3u)
	require.NoError(t, os.MkdirAll(config.GetEPGDirPath(), 0755))
	now := time.Now().UTC()
	epgXML := fmt.Sprintf(`<?xml version="1.0"?><tv>
<programme start="%s +0000" stop="%s +0000" channel="cnn.id"><title>Archived</title></programme>
<programme start="%s +0000" stop="%s +0000" channel="cnn.id"><title>Current</title></programme>
</tv>`,
		now.Add(-3*time.Hour).Format("20060102150405"), now.Add(-2*time.Hour).Format("20060102150405"),
		now.Add(-time.Hour).Format("20060102150405"), now.Add(time.Hour).Format("20060102150405"))
	require.NoError(t, os.WriteFile(config.GetEPGPath(), []byte(epgXML), 0644))

	id := strconv.FormatUint(sourceproc.StreamIDFor("CNN"), 10)
	rec := playerAPIRequest(t, h, "action=get_simple_data_table&stream_id="+id)
	var table map[string][]xtream.EPGListingOut
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &table))
	require.Len(t, table["epg_listings"], 2)
	assert.Equal(t, 1, table["epg_listings"][0].HasArchive)

	rec = playerAPIRequest(t, h, "action=get_short_epg&stream_id="+id)
	var short map[string][]xtream.EPGListingOut
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &short))
	assert.Len(t, short["epg_listings"], 1)
}

func TestXtreamXMLTV(t *testing.T) {
	h := setupXtreamHandler(t)
	require.NoError(t, os.MkdirAll(config.GetEPGDirPath(), 0755))

	serve := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/xmltv.php?username=u&password=p", nil)
		rec := httptest.NewRecorder()
		h.ServeXMLTV(rec, req)
		return rec
	}

	rec := serve()
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "xml")
	assert.Contains(t, rec.Body.String(), "<tv")

	require.NoError(t, os.WriteFile(config.GetEPGPath(),
		[]byte(`<?xml version="1.0"?><tv><channel id="cnn.id"><display-name>CNN</display-name></channel></tv>`), 0644))
	rec = serve()
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "xml")
	assert.Contains(t, rec.Body.String(), `id="cnn.id"`)
}
