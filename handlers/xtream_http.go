package handlers

import (
	"encoding/base64"
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
	"time"

	"m3u-stream-merger/config"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/utils"
	"m3u-stream-merger/xtream"

	"github.com/goccy/go-json"
)

type XtreamHTTPHandler struct {
	logger        logger.Logger
	catalog       *xtream.Catalog
	auth          *CredentialsAuth
	streamHandler *StreamHTTPHandler
}

func NewXtreamHTTPHandler(streamHandler *StreamHTTPHandler, logger logger.Logger) *XtreamHTTPHandler {
	return &XtreamHTTPHandler{
		logger:        logger,
		catalog:       xtream.GetCatalog(),
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
	case "":
		h.writeJSON(w, h.rootResponse(r, query.Get("username"), query.Get("password")))
	case "get_live_categories":
		h.writeJSON(w, h.catalog.Categories(xtream.TypeLive))
	case "get_vod_categories":
		h.writeJSON(w, h.catalog.Categories(xtream.TypeMovie))
	case "get_series_categories":
		h.writeJSON(w, h.catalog.Categories(xtream.TypeSeries))
	case "get_live_streams":
		h.writeJSON(w, h.liveStreams(categoryID))
	case "get_vod_streams":
		h.writeJSON(w, h.vodStreams(categoryID))
	case "get_series":
		h.writeJSON(w, h.seriesList(categoryID))
	case "get_series_info":
		id, _ := strconv.ParseUint(query.Get("series_id"), 10, 64)
		h.writeJSON(w, h.seriesInfo(id))
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
	channels := make(map[string]any)
	for _, s := range h.liveStreams(0) {
		channels[s.StreamID.String()] = s
	}
	for _, s := range h.vodStreams(0) {
		channels[s.StreamID.String()] = s
	}

	h.writeJSON(w, xtream.PanelResponse{
		UserInfo:   root.UserInfo,
		ServerInfo: root.ServerInfo,
		Categories: map[string][]xtream.RawCategory{
			xtream.TypeLive:   h.catalog.Categories(xtream.TypeLive),
			xtream.TypeMovie:  h.catalog.Categories(xtream.TypeMovie),
			xtream.TypeSeries: h.catalog.Categories(xtream.TypeSeries),
		},
		AvailableChannels: channels,
	})
}

func (h *XtreamHTTPHandler) rootResponse(r *http.Request, user, pass string) xtream.RootResponse {
	host := r.Host
	proto := "http"
	if r.TLS != nil {
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
			ActiveCons:           "0",
			CreatedAt:            now.Unix(),
			MaxConnections:       "1",
			AllowedOutputFormats: []string{"m3u8", "ts"},
		},
		ServerInfo: xtream.ServerInfo{
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

func num(i int) int { return i + 1 }

const (
	shortEPGLimit  = 4
	dataTableLimit = 1000
)

func (h *XtreamHTTPHandler) liveStreams(categoryID uint64) []xtream.LiveStreamOut {
	entries := h.catalog.Live(categoryID)
	out := make([]xtream.LiveStreamOut, 0, len(entries))
	for i, e := range entries {
		catID := jsonNumber(h.catalog.CategoryID(xtream.TypeLive, e.Group))
		out = append(out, xtream.LiveStreamOut{
			Num:          num(i),
			Name:         e.Title,
			StreamType:   xtream.TypeLive,
			StreamID:     jsonNumber(e.StreamID),
			StreamIcon:   e.Logo,
			EPGChannelID: e.TvgID,
			Added:        "0",
			IsAdult:      "0",
			CategoryID:   catID,
			CategoryIDs:  []json.Number{catID},
		})
	}
	return out
}

func (h *XtreamHTTPHandler) vodStreams(categoryID uint64) []xtream.VodStreamOut {
	entries := h.catalog.Vod(categoryID)
	out := make([]xtream.VodStreamOut, 0, len(entries))
	for i, e := range entries {
		catID := jsonNumber(h.catalog.CategoryID(xtream.TypeMovie, e.Group))
		out = append(out, xtream.VodStreamOut{
			Num:                num(i),
			Name:               e.Title,
			StreamType:         xtream.TypeMovie,
			StreamID:           jsonNumber(e.StreamID),
			StreamIcon:         e.Logo,
			Added:              "0",
			IsAdult:            "0",
			CategoryID:         catID,
			CategoryIDs:        []json.Number{catID},
			ContainerExtension: containerExt(e.Ext, "mp4"),
		})
	}
	return out
}

func (h *XtreamHTTPHandler) seriesList(categoryID uint64) []xtream.SeriesOut {
	series := h.catalog.SeriesList(categoryID)
	out := make([]xtream.SeriesOut, 0, len(series))
	for i, sd := range series {
		catID := jsonNumber(h.catalog.CategoryID(xtream.TypeSeries, sd.Group))
		out = append(out, xtream.SeriesOut{
			Num:          num(i),
			Name:         sd.Name,
			SeriesID:     jsonNumber(sd.SeriesID),
			Cover:        sd.Cover,
			Genre:        sd.Group,
			BackdropPath: []string{},
			CategoryID:   catID,
			CategoryIDs:  []json.Number{catID},
		})
	}
	return out
}

func containerExt(ext, fallback string) string {
	if trimmed := strings.TrimPrefix(ext, "."); trimmed != "" {
		return trimmed
	}

	return fallback
}

func (h *XtreamHTTPHandler) seriesInfo(id uint64) *xtream.SeriesInfoOut {
	sd := h.catalog.SeriesInfo(id)
	if sd == nil {
		return &xtream.SeriesInfoOut{Seasons: []xtream.SeasonOut{}, Episodes: map[string][]xtream.EpisodeOut{}}
	}

	info := xtream.SeriesInfoOut{
		Seasons:  make([]xtream.SeasonOut, 0, len(sd.Episodes)),
		Episodes: make(map[string][]xtream.EpisodeOut, len(sd.Episodes)),
	}
	info.Info.Name = sd.Name
	info.Info.Cover = sd.Cover
	info.Info.Genre = sd.Group
	info.Info.BackdropPath = []string{}
	info.Info.CategoryID = strconv.FormatUint(h.catalog.CategoryID(xtream.TypeSeries, sd.Group), 10)

	seasons := make([]int, 0, len(sd.Episodes))
	for season := range sd.Episodes {
		seasons = append(seasons, season)
	}
	sort.Ints(seasons)

	for _, season := range seasons {
		key := strconv.Itoa(season)
		for _, ep := range sd.Episodes[season] {
			info.Episodes[key] = append(info.Episodes[key], xtream.EpisodeOut{
				ID:                 jsonNumber(ep.StreamID),
				EpisodeNum:         ep.Episode,
				Title:              ep.Title,
				ContainerExtension: containerExt(ep.Ext, "mkv"),
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

	catID := jsonNumber(h.catalog.CategoryID(xtream.TypeMovie, e.Group))
	ext := containerExt(e.Ext, "mp4")

	return xtream.VodInfoOut{
		Info: xtream.VodInfoDetailOut{
			MovieImage:   e.Logo,
			CoverBig:     e.Logo,
			Name:         e.Title,
			ONname:       e.Title,
			Genre:        e.Group,
			BackdropPath: []string{},
		},
		MovieData: xtream.VodMovieDataOut{
			StreamID:           jsonNumber(e.StreamID),
			Name:               e.Title,
			Added:              "0",
			CategoryID:         catID,
			CategoryIDs:        []json.Number{catID},
			ContainerExtension: ext,
		},
	}
}

type xmltvEPG struct {
	Programmes []struct {
		Start   string `xml:"start,attr"`
		Stop    string `xml:"stop,attr"`
		Channel string `xml:"channel,attr"`
		Title   struct {
			Text string `xml:",chardata"`
			Lang string `xml:"lang,attr"`
		} `xml:"title"`
		Desc string `xml:"desc"`
	} `xml:"programme"`
}

// epgListings reads the merged XMLTV; Xtream clients expect base64 title/desc.
// The simple data table variant flags the entry currently on air.
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

	var tv xmltvEPG
	if err := xml.NewDecoder(file).Decode(&tv); err != nil {
		return empty
	}

	listings := make([]xtream.EPGListingOut, 0, limit)
	for _, p := range tv.Programmes {
		if len(listings) >= limit {
			break
		}
		if p.Channel != entry.TvgID {
			continue
		}
		start, startTS := xmltvTime(p.Start)
		end, endTS := xmltvTime(p.Stop)
		lang := p.Title.Lang
		if lang == "" {
			lang = "en"
		}
		nowPlaying := 0
		if dataTable && len(listings) == 0 {
			nowPlaying = 1
		}
		listings = append(listings, xtream.EPGListingOut{
			ID:             jsonNumber(streamID),
			EPGID:          jsonNumber(streamID),
			Title:          base64.StdEncoding.EncodeToString([]byte(p.Title.Text)),
			Lang:           lang,
			Start:          start,
			End:            end,
			Description:    base64.StdEncoding.EncodeToString([]byte(p.Desc)),
			ChannelID:      entry.TvgID,
			StartTimestamp: startTS,
			StopTimestamp:  endTS,
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

	var sb strings.Builder
	fmt.Fprintf(&sb, "#EXTM3U x-tvg-url=\"%s/xmltv.php?username=%s&password=%s\"\n", baseURL, user, pass)

	appendEntry := func(e *xtream.Entry, urlPath string) {
		attrs := ""
		if e.TvgID != "" {
			attrs += fmt.Sprintf(` tvg-id="%s"`, e.TvgID)
		}
		if e.Logo != "" {
			attrs += fmt.Sprintf(` tvg-logo="%s"`, e.Logo)
		}
		if e.Group != "" {
			attrs += fmt.Sprintf(` tvg-group="%s" group-title="%s"`, e.Group, e.Group)
		}
		fmt.Fprintf(&sb, "#EXTINF:-1%s,%s\n", attrs, e.Title)
		fmt.Fprintf(&sb, "%s/%s\n", baseURL, urlPath)
	}

	for _, e := range h.catalog.Live(0) {
		appendEntry(e, fmt.Sprintf("live/%s/%s/%d%s", user, pass, e.StreamID, liveExt))
	}
	for _, e := range h.catalog.Vod(0) {
		ext := strings.TrimPrefix(e.Ext, ".")
		if ext == "" {
			ext = "mp4"
		}
		appendEntry(e, fmt.Sprintf("movie/%s/%s/%d.%s", user, pass, e.StreamID, ext))
	}
	for _, sd := range h.catalog.SeriesList(0) {
		for _, episodes := range sd.Episodes {
			for _, ep := range episodes {
				ext := strings.TrimPrefix(ep.Ext, ".")
				if ext == "" {
					ext = "mkv"
				}
				appendEntry(ep, fmt.Sprintf("series/%s/%s/%d.%s", user, pass, ep.StreamID, ext))
			}
		}
	}

	w.Header().Set("Content-Type", "application/x-mpegurl")
	_, _ = w.Write([]byte(sb.String()))
}

func (h *XtreamHTTPHandler) ServeXMLTV(w http.ResponseWriter, r *http.Request) {
	if !h.auth.AuthorizeRequest(r) {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	http.ServeFile(w, r, config.GetEPGPath())
}
