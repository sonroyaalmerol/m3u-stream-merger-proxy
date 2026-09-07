package handlers

import (
	"bufio"
	"context"
	"encoding/base64"
	stdjson "encoding/json"
	"encoding/xml"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"m3u-stream-merger/config"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/sourceproc"
	"m3u-stream-merger/utils"
	"m3u-stream-merger/xtream"

	"github.com/goccy/go-json"
)

type XtreamHTTPHandler struct {
	logger        logger.Logger
	catalog       *sourceproc.StreamStore
	auth          *CredentialsAuth
	streamHandler *StreamHTTPHandler
	stubRegistry  seriesStubRegistry
	lazyMu        sync.Mutex
	lazyCache     map[uint64]*lazySeries
	lazyEpisodes  map[uint64]lazyEpisode
	lazyOrder     []uint64
}

func NewXtreamHTTPHandler(streamHandler *StreamHTTPHandler, logger logger.Logger) *XtreamHTTPHandler {
	return &XtreamHTTPHandler{
		logger:        logger,
		catalog:       sourceproc.GetStreamStore(),
		auth:          NewCredentialsAuth(logger),
		streamHandler: streamHandler,
	}
}

func (h *XtreamHTTPHandler) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// deniedResponse is how panels report bad credentials: HTTP 200 with auth 0.
func deniedResponse(user string) xtream.RootResponse {
	return xtream.RootResponse{
		UserInfo: xtream.UserInfo{
			Username: user,
			Message:  "Invalid credentials",
			Auth:     0,
			Status:   "Disabled",
		},
	}
}

func (h *XtreamHTTPHandler) ServePlayerAPI(w http.ResponseWriter, r *http.Request) {
	query := RequestValues(r)
	if !h.auth.Authorize(query.Get("username"), query.Get("password")) {
		h.writeJSON(w, deniedResponse(query.Get("username")))
		return
	}

	action := query.Get("action")
	categoryID, _ := strconv.ParseUint(query.Get("category_id"), 10, 64)

	switch action {
	case "", "get_profile", "get_server_info", "get_account_info":
		h.writeJSON(w, h.rootResponse(r, query.Get("username"), query.Get("password")))
	case "get_live_categories":
		h.writeJSON(w, h.categories(xtream.TypeLive))
	case "get_vod_categories":
		h.writeJSON(w, h.categories(xtream.TypeMovie))
	case "get_series_categories":
		h.writeJSON(w, h.categories(xtream.TypeSeries))
	case "get_live_streams":
		h.writeLiveStreams(w, categoryID)
	case "get_vod_streams":
		h.writeVodStreams(w, categoryID)
	case "get_series":
		h.writeSeriesList(w, categoryID)
	case "get_series_info":
		id, _ := strconv.ParseUint(query.Get("series_id"), 10, 64)
		h.writeJSON(w, h.seriesInfo(r.Context(), id))
	case "get_vod_info":
		id, _ := strconv.ParseUint(query.Get("vod_id"), 10, 64)
		h.writeJSON(w, h.vodInfo(id))
	case "get_short_epg":
		id, _ := strconv.ParseUint(query.Get("stream_id"), 10, 64)
		limit, _ := strconv.Atoi(query.Get("limit"))
		if limit <= 0 {
			limit = shortEPGLimit
		}
		h.writeJSON(w, h.epgListings(id, limit, false))
	case "get_simple_data_table":
		id, _ := strconv.ParseUint(query.Get("stream_id"), 10, 64)
		h.writeJSON(w, h.epgListings(id, dataTableLimit, true))
	default:
		h.writeJSON(w, []any{})
	}
}

// ServePanelAPI answers the legacy panel_api.php probe some clients still make.
func (h *XtreamHTTPHandler) ServePanelAPI(w http.ResponseWriter, r *http.Request) {
	query := RequestValues(r)
	if !h.auth.Authorize(query.Get("username"), query.Get("password")) {
		h.writeJSON(w, deniedResponse(query.Get("username")))
		return
	}

	root := h.rootResponse(r, query.Get("username"), query.Get("password"))
	w.Header().Set("Content-Type", "application/json")
	out := bufio.NewWriterSize(w, 64<<10)
	if err := out.WriteByte('{'); err != nil {
		return
	}
	writeField := func(name string, v any) bool {
		b, err := stdjson.Marshal(v)
		if err != nil {
			return false
		}
		_, err = fmt.Fprintf(out, `"%s":%s,`, name, b)
		return err == nil
	}
	if !writeField("user_info", root.UserInfo) ||
		!writeField("server_info", root.ServerInfo) ||
		!writeField("categories", map[string][]xtream.RawCategory{
			xtream.TypeLive:   h.categories(xtream.TypeLive),
			xtream.TypeMovie:  h.categories(xtream.TypeMovie),
			xtream.TypeSeries: h.categories(xtream.TypeSeries),
		}) {
		return
	}
	if _, err := out.WriteString(`"available_channels":{`); err != nil {
		return
	}
	first := true
	emitChannel := func(key string, v any) bool {
		if !first {
			if err := out.WriteByte(','); err != nil {
				return false
			}
		}
		first = false
		b, err := stdjson.Marshal(v)
		if err != nil {
			return false
		}
		_, err = fmt.Fprintf(out, `"%s":%s`, key, b)
		return err == nil
	}
	_ = h.catalog.RangeEntries(xtream.TypeLive, 0, func(_ int, e sourceproc.CatalogEntry) bool {
		return emitChannel(idStr(e.StreamID), liveStreamOut(0, e))
	})
	_ = h.catalog.RangeEntries(xtream.TypeMovie, 0, func(_ int, e sourceproc.CatalogEntry) bool {
		return emitChannel(idStr(e.StreamID), vodStreamOut(0, e))
	})
	if _, err := out.WriteString("}}\n"); err != nil {
		return
	}
	_ = out.Flush()
}

func (h *XtreamHTTPHandler) rootResponse(r *http.Request, user, pass string) xtream.RootResponse {
	host := r.Host
	proto := "http"
	if r.TLS != nil || utils.IsForwardedHTTPS(r) {
		proto = "https"
	}
	if base := os.Getenv("BASE_URL"); base != "" {
		if parsed, err := url.Parse(base); err == nil && parsed.Host != "" {
			host = parsed.Host
			if parsed.Scheme == "https" {
				proto = "https"
			}
		}
	}

	serverHost, serverPort := host, ""
	if h_, p_, err := net.SplitHostPort(host); err == nil {
		serverHost, serverPort = h_, p_
	} else if proto == "https" {
		serverPort = "443"
	} else {
		serverPort = "80"
	}

	tz := os.Getenv("TZ")
	if tz == "" {
		tz = "UTC"
	}
	now := time.Now()

	return xtream.RootResponse{
		UserInfo: xtream.UserInfo{
			Username:             user,
			Password:             pass,
			Message:              "M3U Stream Merger Proxy",
			Auth:                 1,
			Status:               "Active",
			ExpDate:              "0",
			IsTrial:              "0",
			ActiveCons:           0,
			CreatedAt:            strconv.FormatInt(now.Unix(), 10),
			MaxConnections:       "1",
			AllowedOutputFormats: []string{"m3u8", "ts"},
		},
		ServerInfo: xtream.ServerInfo{
			Xui:            true,
			Version:        "1.5.5",
			URL:            serverHost,
			Port:           serverPort,
			HTTPSPort:      "443",
			RTMPPort:       "0",
			ServerProtocol: proto,
			Timezone:       tz,
			TimestampNow:   now.Unix(),
			TimeNow:        now.Format("2006-01-02 15:04:05"),
		},
	}
}

const (
	shortEPGLimit  = 4
	dataTableLimit = 1000
)

func (h *XtreamHTTPHandler) categories(kind string) []xtream.RawCategory {
	categories := h.catalog.Categories(kind)
	out := make([]xtream.RawCategory, 0, len(categories))
	for _, category := range categories {
		out = append(out, xtream.RawCategory{
			CategoryID:   idStr(category.ID),
			CategoryName: category.Name,
			ParentID:     0,
		})
	}
	return out
}

// writeJSONArray streams v as a JSON array one entry at a time, so catalog-sized
// responses (hundreds of thousands of entries) never buffer whole-payload in RAM.
// Iteration stops on client disconnect; the connection is already broken then.
func writeJSONArray[T any](w http.ResponseWriter, iter func(yield func(T) bool)) bool {
	w.Header().Set("Content-Type", "application/json")
	out := bufio.NewWriterSize(w, 64<<10)
	if err := out.WriteByte('['); err != nil {
		return false
	}
	first := true
	iter(func(v T) bool {
		if !first {
			if err := out.WriteByte(','); err != nil {
				return false
			}
		}
		first = false
		b, err := stdjson.Marshal(v)
		if err != nil {
			return false
		}
		_, err = out.Write(b)
		return err == nil
	})
	_, err := out.WriteString("]\n")
	return err == nil && out.Flush() == nil
}

func liveStreamOut(position int, e sourceproc.CatalogEntry) xtream.LiveStreamOut {
	catID := jsonNumber(e.CategoryID)
	return xtream.LiveStreamOut{
		Num:          position,
		Name:         e.Title,
		StreamType:   xtream.TypeLive,
		StreamID:     jsonNumber(e.StreamID),
		StreamIcon:   e.Logo,
		EPGChannelID: e.TvgID,
		Added:        "0",
		IsAdult:      "0",
		CategoryID:   string(catID),
		CategoryIDs:  []json.Number{catID},
	}
}

func vodStreamOut(position int, e sourceproc.CatalogEntry) xtream.VodStreamOut {
	catID := jsonNumber(e.CategoryID)
	return xtream.VodStreamOut{
		Num:                position,
		Name:               e.Title,
		Title:              e.Title,
		StreamType:         xtream.TypeMovie,
		StreamID:           jsonNumber(e.StreamID),
		StreamIcon:         e.Logo,
		Genre:              e.Group,
		Added:              "0",
		IsAdult:            "0",
		CategoryID:         string(catID),
		CategoryIDs:        []json.Number{catID},
		ContainerExtension: containerExt(e.Ext, "mp4"),
	}
}

func (h *XtreamHTTPHandler) writeLiveStreams(w http.ResponseWriter, categoryID uint64) {
	writeJSONArray(w, func(yield func(xtream.LiveStreamOut) bool) {
		_ = h.catalog.RangeEntries(xtream.TypeLive, categoryID, func(position int, e sourceproc.CatalogEntry) bool {
			return yield(liveStreamOut(position, e))
		})
	})
}

func (h *XtreamHTTPHandler) writeVodStreams(w http.ResponseWriter, categoryID uint64) {
	writeJSONArray(w, func(yield func(xtream.VodStreamOut) bool) {
		_ = h.catalog.RangeEntries(xtream.TypeMovie, categoryID, func(position int, e sourceproc.CatalogEntry) bool {
			return yield(vodStreamOut(position, e))
		})
	})
}

func (h *XtreamHTTPHandler) writeSeriesList(w http.ResponseWriter, categoryID uint64) {
	writeJSONArray(w, func(yield func(xtream.SeriesOut) bool) {
		seen := make(map[uint64]struct{})
		num := 0
		_ = h.catalog.RangeSeries(categoryID, func(position int, sd sourceproc.CatalogSeriesEntry) bool {
			seen[sd.SeriesID] = struct{}{}
			catID := jsonNumber(sd.CategoryID)
			num = position
			return yield(xtream.SeriesOut{
				Num:          position,
				Name:         sd.Name,
				Title:        sd.Name,
				StreamType:   xtream.TypeSeries,
				SeriesID:     jsonNumber(sd.SeriesID),
				Cover:        sd.Cover,
				Genre:        sd.Group,
				BackdropPath: []string{},
				CategoryID:   string(catID),
				CategoryIDs:  []json.Number{catID},
			})
		})
		var stubOnly []*stubSeries
		for id, st := range h.stubs() {
			if _, ok := seen[id]; ok {
				continue
			}
			if categoryID != 0 && st.CategoryID != categoryID {
				continue
			}
			stubOnly = append(stubOnly, st)
		}
		sort.Slice(stubOnly, func(a, b int) bool { return stubOnly[a].SeriesID < stubOnly[b].SeriesID })
		for _, st := range stubOnly {
			catID := jsonNumber(st.CategoryID)
			num++
			if !yield(xtream.SeriesOut{
				Num:          num,
				Name:         st.Name,
				Title:        st.Name,
				StreamType:   xtream.TypeSeries,
				SeriesID:     jsonNumber(st.SeriesID),
				Cover:        st.Cover,
				Genre:        st.Group,
				BackdropPath: []string{},
				CategoryID:   string(catID),
				CategoryIDs:  []json.Number{catID},
			}) {
				return
			}
		}
	})
}

func containerExt(ext, fallback string) string {
	if trimmed := strings.TrimPrefix(ext, "."); trimmed != "" {
		return trimmed
	}

	return fallback
}

func (h *XtreamHTTPHandler) seriesInfo(ctx context.Context, id uint64) *xtream.SeriesInfoOut {
	sd := h.catalog.SeriesInfo(id)
	if sd == nil {
		if ls := h.lazySeriesInfo(ctx, id); ls != nil {
			return ls
		}
		return &xtream.SeriesInfoOut{Seasons: []xtream.SeasonOut{}, Episodes: map[string][]xtream.EpisodeOut{}}
	}

	info := xtream.SeriesInfoOut{
		Seasons:  make([]xtream.SeasonOut, 0, len(sd.Episodes)),
		Episodes: make(map[string][]xtream.EpisodeOut, len(sd.Episodes)),
	}
	info.Info.Name = sd.Name
	info.Info.Title = sd.Name
	info.Info.SeriesID = jsonNumber(id)
	info.Info.Cover = sd.Cover
	info.Info.Genre = sd.Group
	info.Info.BackdropPath = []string{}
	info.Info.CategoryID = idStr(sd.CategoryID)
	info.Info.CategoryIDs = []json.Number{jsonNumber(sd.CategoryID)}

	seasons := make([]int, 0, len(sd.Episodes))
	for season := range sd.Episodes {
		seasons = append(seasons, season)
	}
	sort.Ints(seasons)

	for _, season := range seasons {
		key := strconv.Itoa(season)
		for _, ep := range sd.Episodes[season] {
			info.Episodes[key] = append(info.Episodes[key], xtream.EpisodeOut{
				ID:                 idStr(ep.StreamID),
				EpisodeNum:         strconv.Itoa(ep.Episode),
				Title:              ep.Title,
				ContainerExtension: containerExt(ep.Ext, "mkv"),
				Subtitles:          []string{},
				Added:              "0",
				Season:             season,
				Info: xtream.EpisodeInfoOut{
					MovieImage: ep.Logo,
					Season:     season,
				},
			})
		}

		info.Seasons = append(info.Seasons, xtream.SeasonOut{
			ID:           jsonNumber(uint64(season)),
			Name:         "Season " + key,
			SeasonNumber: season,
			EpisodeCount: len(info.Episodes[key]),
			Cover:        sd.Cover,
			CoverBig:     sd.Cover,
		})
	}

	return &info
}

func (h *XtreamHTTPHandler) vodInfo(id uint64) xtream.VodInfoOut {
	e := h.catalog.FindStream(id)
	if e == nil {
		return xtream.VodInfoOut{}
	}

	catID := jsonNumber(e.CategoryID)
	ext := containerExt(e.Ext, "mp4")

	return xtream.VodInfoOut{
		Info: xtream.VodInfoDetailOut{
			MovieImage:   e.Logo,
			CoverBig:     e.Logo,
			Name:         e.Title,
			ONname:       e.Title,
			Genre:        e.Group,
			BackdropPath: []string{},
			Subtitles:    []string{},
		},
		MovieData: xtream.VodMovieDataOut{
			StreamID:           jsonNumber(e.StreamID),
			Name:               e.Title,
			Title:              e.Title,
			Added:              "0",
			CategoryID:         string(catID),
			CategoryIDs:        []json.Number{catID},
			ContainerExtension: ext,
		},
	}
}

type xmltvProgramme struct {
	Start   string `xml:"start,attr"`
	Stop    string `xml:"stop,attr"`
	Channel string `xml:"channel,attr"`
	Title   struct {
		Text string `xml:",chardata"`
		Lang string `xml:"lang,attr"`
	} `xml:"title"`
	Desc string `xml:"desc"`
}

// epgListings streams the merged XMLTV; Xtream clients expect base64 title/desc.
// The simple data table variant flags the entry currently on air. The file is
// scanned with a streaming decoder and only matching programmes are retained,
// so memory stays bounded by the requested limit instead of the EPG size.
func (h *XtreamHTTPHandler) epgListings(streamID uint64, limit int, dataTable bool) map[string]any {
	empty := map[string]any{"epg_listings": []xtream.EPGListingOut{}}

	entry := h.catalog.FindStream(streamID)
	if entry == nil || entry.TvgID == "" {
		return empty
	}

	file, err := os.Open(config.GetEPGPath())
	if err != nil {
		return empty
	}
	defer func() { _ = file.Close() }()

	dec := xml.NewDecoder(bufio.NewReaderSize(file, 128<<10))
	listings := make([]xtream.EPGListingOut, 0, limit)
	var p xmltvProgramme
	for len(listings) < limit {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "programme" {
			continue
		}
		if err := dec.DecodeElement(&p, &start); err != nil {
			break
		}
		if p.Channel != entry.TvgID {
			continue
		}
		startStr, startTS := xmltvTime(p.Start)
		end, endTS := xmltvTime(p.Stop)
		lang := p.Title.Lang
		if lang == "" {
			lang = "en"
		}
		nowPlaying := 0
		if unixNow := time.Now().Unix(); startTS <= unixNow && unixNow < endTS {
			nowPlaying = 1
		}
		listings = append(listings, xtream.EPGListingOut{
			ID:             idStr(sourceproc.StreamIDFor(entry.TvgID + "|" + p.Start)),
			EPGID:          idStr(streamID),
			Title:          base64.StdEncoding.EncodeToString([]byte(p.Title.Text)),
			Lang:           lang,
			Start:          startStr,
			End:            end,
			Description:    base64.StdEncoding.EncodeToString([]byte(p.Desc)),
			ChannelID:      entry.TvgID,
			StartTimestamp: strconv.FormatInt(startTS, 10),
			StopTimestamp:  strconv.FormatInt(endTS, 10),
			Stop:           end,
			NowPlaying:     nowPlaying,
		})
	}

	return map[string]any{"epg_listings": listings}
}

// xmltvTime converts "20240101120000 +0000" to "2024-01-01 12:00:00" plus unix seconds.
func xmltvTime(v string) (string, int64) {
	raw := strings.TrimSpace(strings.SplitN(v, " ", 2)[0])
	if t, err := time.Parse("20060102150405 -0700", v); err == nil {
		return t.Format("2006-01-02 15:04:05"), t.Unix()
	}
	if t, err := time.Parse("20060102150405", raw); err == nil {
		return t.Format("2006-01-02 15:04:05"), t.Unix()
	}
	return v, 0
}

func jsonNumber(v uint64) json.Number {
	return json.Number(strconv.FormatUint(v, 10))
}

// idStr renders an id for the fields panels send as JSON strings, not numbers.
func idStr(v uint64) string {
	return strconv.FormatUint(v, 10)
}

// ServeStream handles /live/{user}/{pass}/{id}.{ext} style endpoints by
// rewriting to /p/{basePath}/{slug}.{ext} and delegating to the stream handler.
func (h *XtreamHTTPHandler) ServeStream(w http.ResponseWriter, r *http.Request) {
	segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(segments) != 4 {
		http.Error(w, "Invalid stream path", http.StatusBadRequest)
		return
	}

	user, pass := segments[1], segments[2]
	if !h.auth.Authorize(user, pass) {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}

	idPart := segments[3]
	idStr := idPart
	requestedExt := path.Ext(idPart)
	if requestedExt != "" {
		idStr = strings.TrimSuffix(idPart, requestedExt)
	}
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid stream id", http.StatusBadRequest)
		return
	}

	entry := h.catalog.FindStream(id)
	if entry == nil || entry.Slug == "" {
		if h.serveLazyEpisode(w, r, id, user, pass) {
			return
		}
		http.Error(w, "Stream not found", http.StatusNotFound)
		return
	}

	ext := entry.Ext
	if ext == "" {
		if requestedExt != "" {
			ext = requestedExt
		} else {
			ext = ".ts"
		}
	}

	h.logger.Debugf("Xtream stream %d -> slug %s", id, entry.Slug)
	r.URL.Path = "/p/" + entry.BasePath + "/" + entry.Slug + ext
	h.streamHandler.ServeHTTP(w, r)
}

// ServeGetPHP exports the catalog as an Xtream-style M3U playlist.
func (h *XtreamHTTPHandler) ServeGetPHP(w http.ResponseWriter, r *http.Request) {
	if !h.auth.AuthorizeRequest(r) {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}

	query := r.URL.Query()
	user, pass := query.Get("username"), query.Get("password")
	baseURL := utils.DetermineBaseURL(r)
	liveExt := ".ts"
	if query.Get("output") == "m3u8" {
		liveExt = ".m3u8"
	}

	w.Header().Set("Content-Type", "application/x-mpegurl")
	out := bufio.NewWriterSize(w, 64<<10)
	if _, err := fmt.Fprintf(out, "#EXTM3U x-tvg-url=\"%s/xmltv.php?username=%s&password=%s\"\n", baseURL, user, pass); err != nil {
		return
	}

	appendEntry := func(e sourceproc.CatalogEntry, streamType, ext string) bool {
		if _, err := fmt.Fprint(out, "#EXTINF:-1"); err != nil {
			return false
		}
		if e.TvgID != "" {
			if _, err := fmt.Fprintf(out, ` tvg-id="%s"`, e.TvgID); err != nil {
				return false
			}
		}
		if e.Logo != "" {
			if _, err := fmt.Fprintf(out, ` tvg-logo="%s"`, e.Logo); err != nil {
				return false
			}
		}
		if e.Group != "" {
			if _, err := fmt.Fprintf(out, ` tvg-group="%s" group-title="%s"`, e.Group, e.Group); err != nil {
				return false
			}
		}
		if _, err := fmt.Fprintf(out, ",%s\n%s/%s/%s/%s/%d%s\n", e.Title, baseURL, streamType, user, pass, e.StreamID, ext); err != nil {
			return false
		}
		return true
	}

	_ = h.catalog.RangeEntries(xtream.TypeLive, 0, func(_ int, e sourceproc.CatalogEntry) bool {
		return appendEntry(e, "live", liveExt)
	})
	_ = h.catalog.RangeEntries(xtream.TypeMovie, 0, func(_ int, e sourceproc.CatalogEntry) bool {
		return appendEntry(e, "movie", "."+containerExt(e.Ext, "mp4"))
	})
	_ = h.catalog.RangeEntries(xtream.TypeSeries, 0, func(_ int, e sourceproc.CatalogEntry) bool {
		return appendEntry(e, "series", "."+containerExt(e.Ext, "mkv"))
	})
	_ = out.Flush()
}

func (h *XtreamHTTPHandler) ServeXMLTV(w http.ResponseWriter, r *http.Request) {
	if !h.auth.AuthorizeRequest(r) {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	http.ServeFile(w, r, config.GetEPGPath())
}
