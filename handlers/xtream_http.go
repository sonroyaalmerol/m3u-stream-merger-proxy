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

func (h *XtreamHTTPHandler) ServePlayerAPI(w http.ResponseWriter, r *http.Request) {
	if !h.auth.AuthorizeRequest(r) {
		w.Header().Set("WWW-Authenticate", `Basic realm="xtream"`)
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}

	query := r.URL.Query()
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
	case "get_short_epg", "get_simple_data_table":
		id, _ := strconv.ParseUint(query.Get("stream_id"), 10, 64)
		limit, _ := strconv.Atoi(query.Get("limit"))
		if limit <= 0 {
			limit = 24
		}
		h.writeJSON(w, h.shortEPG(id, limit))
	default:
		h.writeJSON(w, []any{})
	}
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
			AllowedOutputFormats: []string{"ts"},
		},
		ServerInfo: xtream.ServerInfo{
			URL:            serverHost,
			Port:           serverPort,
			HTTPSPort:      "443",
			ServerProtocol: proto,
			Timezone:       tz,
			TimestampNow:   now.Unix(),
			TimeNow:        now.Format("2006-01-02 15:04:05"),
		},
	}
}

func num(i int) int { return i + 1 }

func (h *XtreamHTTPHandler) liveStreams(categoryID uint64) []xtream.RawLiveStream {
	entries := h.catalog.Live(categoryID)
	out := make([]xtream.RawLiveStream, 0, len(entries))
	for i, e := range entries {
		out = append(out, xtream.RawLiveStream{
			Num:          num(i),
			Name:         e.Title,
			StreamType:   xtream.TypeLive,
			StreamID:     jsonNumber(e.StreamID),
			StreamIcon:   e.Logo,
			EPGChannelID: e.TvgID,
			Added:        "0",
			CategoryID:   jsonNumber(h.catalog.CategoryID(xtream.TypeLive, e.Group)),
		})
	}
	return out
}

func (h *XtreamHTTPHandler) vodStreams(categoryID uint64) []xtream.RawVodStream {
	entries := h.catalog.Vod(categoryID)
	out := make([]xtream.RawVodStream, 0, len(entries))
	for i, e := range entries {
		ext := strings.TrimPrefix(e.Ext, ".")
		if ext == "" {
			ext = "mp4"
		}
		out = append(out, xtream.RawVodStream{
			Num:                num(i),
			Name:               e.Title,
			StreamType:         xtream.TypeMovie,
			StreamID:           jsonNumber(e.StreamID),
			StreamIcon:         e.Logo,
			Added:              "0",
			CategoryID:         jsonNumber(h.catalog.CategoryID(xtream.TypeMovie, e.Group)),
			ContainerExtension: ext,
		})
	}
	return out
}

func (h *XtreamHTTPHandler) seriesList(categoryID uint64) []xtream.RawSeries {
	series := h.catalog.SeriesList(categoryID)
	out := make([]xtream.RawSeries, 0, len(series))
	for i, sd := range series {
		out = append(out, xtream.RawSeries{
			Num:        num(i),
			Name:       sd.Name,
			SeriesID:   jsonNumber(sd.SeriesID),
			Cover:      sd.Cover,
			CategoryID: jsonNumber(h.catalog.CategoryID(xtream.TypeSeries, sd.Group)),
		})
	}
	return out
}

func (h *XtreamHTTPHandler) seriesInfo(id uint64) *xtream.RawSeriesInfo {
	sd := h.catalog.SeriesInfo(id)
	if sd == nil {
		return &xtream.RawSeriesInfo{}
	}

	info := xtream.RawSeriesInfo{Episodes: make(map[string][]xtream.RawEpisode)}
	info.Info.Name = sd.Name
	info.Info.Cover = sd.Cover
	info.Info.Genre = sd.Group

	seasons := make([]int, 0, len(sd.Episodes))
	for season := range sd.Episodes {
		seasons = append(seasons, season)
	}
	sort.Ints(seasons)

	for _, season := range seasons {
		key := strconv.Itoa(season)
		for _, ep := range sd.Episodes[season] {
			ext := strings.TrimPrefix(ep.Ext, ".")
			if ext == "" {
				ext = "mkv"
			}
			info.Episodes[key] = append(info.Episodes[key], xtream.RawEpisode{
				ID:                 jsonNumber(ep.StreamID),
				EpisodeNum:         ep.Episode,
				Title:              ep.Title,
				ContainerExtension: ext,
				Season:             season,
				MovieImage:         ep.Logo,
			})
		}
	}

	return &info
}

func (h *XtreamHTTPHandler) vodInfo(id uint64) map[string]any {
	e := h.catalog.FindStream(id)
	if e == nil {
		return map[string]any{}
	}
	info := map[string]any{
		"movie_image":         e.Logo,
		"name":                e.Title,
		"genre":               e.Group,
		"container_extension": "mp4",
	}
	if ext := strings.TrimPrefix(e.Ext, "."); ext != "" {
		info["container_extension"] = ext
	}
	return info
}

type xmltvEPG struct {
	Programmes []struct {
		Start   string `xml:"start,attr"`
		Stop    string `xml:"stop,attr"`
		Channel string `xml:"channel,attr"`
		Title   string `xml:"title"`
		Desc    string `xml:"desc"`
	} `xml:"programme"`
}

type epgListing struct {
	ID          string `json:"id"`
	EPGID       string `json:"epg_id"`
	Title       string `json:"title"`
	Lang        string `json:"lang"`
	Start       string `json:"start"`
	End         string `json:"end"`
	Description string `json:"description"`
}

// shortEPG reads the merged XMLTV; Xtream clients expect base64 title/desc.
func (h *XtreamHTTPHandler) shortEPG(streamID uint64, limit int) map[string]any {
	empty := map[string]any{"epg_listings": []epgListing{}}

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

	listings := make([]epgListing, 0, limit)
	for _, p := range tv.Programmes {
		if len(listings) >= limit {
			break
		}
		if p.Channel != entry.TvgID {
			continue
		}
		listings = append(listings, epgListing{
			ID:          strconv.FormatUint(streamID, 10),
			EPGID:       p.Channel,
			Title:       base64.StdEncoding.EncodeToString([]byte(p.Title)),
			Lang:        "en",
			Start:       xmltvTime(p.Start),
			End:         xmltvTime(p.Stop),
			Description: base64.StdEncoding.EncodeToString([]byte(p.Desc)),
		})
	}

	return map[string]any{"epg_listings": listings}
}

// xmltvTime converts "20240101120000 +0000" to "2024-01-01 12:00:00".
func xmltvTime(v string) string {
	raw := strings.TrimSpace(strings.SplitN(v, " ", 2)[0])
	if t, err := time.Parse("20060102150405", raw); err == nil {
		return t.Format("2006-01-02 15:04:05")
	}
	return v
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

	var sb strings.Builder
	sb.WriteString("#EXTM3U\n")

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
		appendEntry(e, fmt.Sprintf("live/%s/%s/%d.ts", user, pass, e.StreamID))
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
