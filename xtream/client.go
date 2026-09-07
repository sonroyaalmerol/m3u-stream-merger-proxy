package xtream

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"m3u-stream-merger/logger"
	"m3u-stream-merger/utils"
)

// Client talks to an upstream Xtream Codes panel via player_api.php.
type Client struct {
	Host     string
	Username string
	Password string
}

func NewClient(host, username, password string) *Client {
	return &Client{
		Host:     strings.TrimSuffix(strings.TrimSpace(host), "/"),
		Username: username,
		Password: password,
	}
}

func (c *Client) apiURL(action string, extra url.Values) string {
	v := url.Values{}
	v.Set("username", c.Username)
	v.Set("password", c.Password)
	if action != "" {
		v.Set("action", action)
	}
	for key, vals := range extra {
		for _, val := range vals {
			v.Add(key, val)
		}
	}
	return c.Host + "/player_api.php?" + v.Encode()
}

func fetchTimeout() time.Duration {
	if v := os.Getenv("XTREAM_FETCH_TIMEOUT"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return 5 * time.Minute
}

// callOnce performs one API attempt; the bool marks truncated 200s and 5xx as retryable.
func callOnce(ctx context.Context, c *Client, action string, extra url.Values, consume func(io.Reader) error) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiURL(action, extra), nil)
	if err != nil {
		return false, err
	}

	resp, err := utils.HTTPClient.Do(req)
	if err != nil {
		return false, err
	}
	if resp.StatusCode >= 500 {
		_ = resp.Body.Close()
		return true, fmt.Errorf("xtream api %s returned status %d", action, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return false, fmt.Errorf("xtream api %s returned status %d", action, resp.StatusCode)
	}

	consumeErr := consume(resp.Body)
	_ = resp.Body.Close()
	if consumeErr != nil {
		if ctx.Err() != nil {
			return false, fmt.Errorf("xtream api %s: %w", action, ctx.Err())
		}
		return retryableJSONError(consumeErr), fmt.Errorf("xtream api %s decode error: %w", action, consumeErr)
	}
	return false, nil
}

func fetchOnce[T any](ctx context.Context, c *Client, action string, extra url.Values) (*T, bool, error) {
	var result T
	retryable, err := callOnce(ctx, c, action, extra, func(body io.Reader) error {
		return decodeAPIResponse(body, &result)
	})
	if err != nil {
		return nil, retryable, err
	}
	return &result, false, nil
}

// fetchAPI retries transient panel failures: truncated 200s and 5xx.
// Per-item actions (get_series_info) must call fetchOnce instead: retrying
// thousands of per-series calls multiplies a panel outage into hours.
func fetchAPI[T any](ctx context.Context, c *Client, action string, extra url.Values) (*T, error) {
	for attempt := 1; ; attempt++ {
		result, retryable, err := fetchOnce[T](ctx, c, action, extra)
		if err == nil || !retryable || attempt >= 3 {
			return result, err
		}
		logger.Default.Warnf("xtream api %s attempt %d/3 failed, retrying: %v", action, attempt, err)
		delay := time.Duration(attempt) * time.Second
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
}

func fetchList[T any](ctx context.Context, c *Client, action string) ([]T, error) {
	result, err := fetchAPI[listResponse[T]](ctx, c, action, nil)
	if err != nil {
		return nil, err
	}
	return []T(*result), nil
}

// fetchStream hands rows to fn as they decode. A retry is only safe before the
// first row escapes: replaying a half-consumed list would duplicate entries.
func fetchStream[T any](ctx context.Context, c *Client, action string, fn func(*T) error) error {
	delivered := 0
	for attempt := 1; ; attempt++ {
		before := delivered
		retryable, err := callOnce(ctx, c, action, nil, func(body io.Reader) error {
			return streamAPIList(body, func(value *T) error {
				delivered++
				return fn(value)
			})
		})
		if err == nil || !retryable || delivered != before || attempt >= 3 {
			return err
		}
		logger.Default.Warnf("xtream api %s attempt %d/3 failed, retrying: %v", action, attempt, err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt) * time.Second):
		}
	}
}

func collectStream[T any](ctx context.Context, c *Client, action string) ([]T, error) {
	var out []T
	err := fetchStream(ctx, c, action, func(value *T) error {
		out = append(out, *value)
		return nil
	})
	return out, err
}

func (c *Client) LiveCategories(ctx context.Context) ([]RawCategory, error) {
	return fetchList[RawCategory](ctx, c, "get_live_categories")
}

func (c *Client) VodCategories(ctx context.Context) ([]RawCategory, error) {
	return fetchList[RawCategory](ctx, c, "get_vod_categories")
}

func (c *Client) SeriesCategories(ctx context.Context) ([]RawCategory, error) {
	return fetchList[RawCategory](ctx, c, "get_series_categories")
}

func (c *Client) LiveStreams(ctx context.Context) ([]RawLiveStream, error) {
	return collectStream[RawLiveStream](ctx, c, "get_live_streams")
}

func (c *Client) VodStreams(ctx context.Context) ([]RawVodStream, error) {
	return collectStream[RawVodStream](ctx, c, "get_vod_streams")
}

func (c *Client) SeriesList(ctx context.Context) ([]RawSeries, error) {
	return collectStream[RawSeries](ctx, c, "get_series")
}

func (c *Client) SeriesInfo(ctx context.Context, seriesID string) (*RawSeriesInfo, error) {
	result, _, err := fetchOnce[RawSeriesInfo](ctx, c, "get_series_info", url.Values{"series_id": {seriesID}})
	return result, err
}

// categoryMap converts the category list into an id -> name lookup. Duplicate
// names across ids are tolerated: the first id wins.
func categoryMap(categories []RawCategory) map[string]string {
	m := make(map[string]string, len(categories))
	for _, cat := range categories {
		if _, ok := m[cat.CategoryID]; !ok {
			m[cat.CategoryID] = cat.CategoryName
		}
	}
	return m
}

// EpisodeTitle mints the synthetic episode title; catalog IDs derive from it, so any change re-keys every episode.
func EpisodeTitle(seriesName, seasonNum string, epNum int) string {
	return fmt.Sprintf("%s S%sE%d", seriesName, strings.TrimPrefix(seasonNum, "0"), epNum)
}

func entryPair(title, group, tvgType, logo, tvgID, streamURL string) (string, string) {
	attrs := fmt.Sprintf(`tvg-name="%s"`, escapeAttr(title))
	if tvgID != "" {
		attrs += fmt.Sprintf(` tvg-id="%s"`, escapeAttr(tvgID))
	}
	attrs += fmt.Sprintf(` tvg-type="%s"`, tvgType)
	if logo != "" {
		attrs += fmt.Sprintf(` tvg-logo="%s"`, escapeAttr(logo))
	}
	if group != "" {
		attrs += fmt.Sprintf(` tvg-group="%s" group-title="%s"`, escapeAttr(group), escapeAttr(group))
	}
	return fmt.Sprintf("#EXTINF:-1 %s,%s", attrs, title), streamURL
}

// SeriesToLines renders one fetched series into the fragment M3U lines; must stay identical to the live emit path.
func SeriesToLines(c *Client, seriesName, group string, info *RawSeriesInfo) []string {
	var lines []string
	for seasonNum, episodes := range info.Episodes {
		for i, ep := range episodes {
			if !usableEntry(seriesName, ep.ID) {
				continue
			}
			epNum := ep.EpisodeNum.Int()
			if epNum <= 0 {
				epNum = i + 1
			}
			ext := ep.ContainerExtension
			if ext == "" {
				ext = "mkv"
			}
			streamURL := fmt.Sprintf("%s/series/%s/%s/%s.%s", c.Host, c.Username, c.Password, ep.ID.String(), ext)
			inf, u := entryPair(EpisodeTitle(seriesName, seasonNum, epNum), group, "series", ep.Image(), "", streamURL)
			lines = append(lines, inf, u)
		}
	}
	return lines
}

type SeriesCachePaths struct {
	Stubs string
	Frag  string
}

// FetchPlaylistLines emits M3U lines in stable section order (live, vod, cached series).
// Ingest is exactly 6 upstream calls; series episode fan-out lives in the background populate loop.
// The three large lists stream one at a time: decoding them concurrently held
// three multi-hundred-MB responses plus their slices per source, which OOMs.
func FetchPlaylistLines(ctx context.Context, c *Client, cache *SeriesCachePaths, emit func(line string) error) error {
	var (
		liveCats, vodCats, seriesCats []RawCategory
		liveCatsErr, vodCatsErr       error
		seriesCatsErr                 error
		fetchWg                       sync.WaitGroup
	)
	started := time.Now()
	fetchWg.Go(func() { liveCats, liveCatsErr = c.LiveCategories(ctx) })
	fetchWg.Go(func() { vodCats, vodCatsErr = c.VodCategories(ctx) })
	fetchWg.Go(func() { seriesCats, seriesCatsErr = c.SeriesCategories(ctx) })
	fetchWg.Wait()

	if liveCatsErr != nil {
		return fmt.Errorf("get_live_categories: %w", liveCatsErr)
	}
	if vodCatsErr != nil {
		return fmt.Errorf("get_vod_categories: %w", vodCatsErr)
	}
	if seriesCatsErr != nil {
		return fmt.Errorf("get_series_categories: %w", seriesCatsErr)
	}

	liveNames := categoryMap(liveCats)
	vodNames := categoryMap(vodCats)
	seriesNames := categoryMap(seriesCats)

	writeEntry := func(title, group, tvgType, logo, tvgID, streamURL string) error {
		extinf, u := entryPair(title, group, tvgType, logo, tvgID, streamURL)
		if err := emit(extinf); err != nil {
			return err
		}
		return emit(u)
	}

	liveCount, skippedLive := 0, 0
	if err := fetchStream(ctx, c, "get_live_streams", func(s *RawLiveStream) error {
		if !usableEntry(s.Name, s.StreamID) {
			skippedLive++
			return nil
		}
		liveCount++
		streamURL := fmt.Sprintf("%s/live/%s/%s/%s.ts", c.Host, c.Username, c.Password, s.StreamID.String())
		return writeEntry(s.Name, liveNames[s.CategoryID.String()], "live", s.StreamIcon, s.EPGChannelID, streamURL)
	}); err != nil {
		return fmt.Errorf("get_live_streams: %w", err)
	}
	if skippedLive > 0 {
		logger.Default.Warnf("Xtream: skipped %d live entries without a name or valid stream_id", skippedLive)
	}

	vodCount, skippedVod := 0, 0
	if err := fetchStream(ctx, c, "get_vod_streams", func(s *RawVodStream) error {
		if !usableEntry(s.Name, s.StreamID) {
			skippedVod++
			return nil
		}
		vodCount++
		ext := s.ContainerExtension
		if ext == "" {
			ext = "mp4"
		}
		streamURL := fmt.Sprintf("%s/movie/%s/%s/%s.%s", c.Host, c.Username, c.Password, s.StreamID.String(), ext)
		return writeEntry(s.Name, vodNames[s.CategoryID.String()], "movie", s.StreamIcon, "", streamURL)
	}); err != nil {
		return fmt.Errorf("get_vod_streams: %w", err)
	}
	if skippedVod > 0 {
		logger.Default.Warnf("Xtream: skipped %d vod entries without a name or valid stream_id", skippedVod)
	}

	stubs := make([]SeriesStub, 0, 1024)
	if err := fetchStream(ctx, c, "get_series", func(s *RawSeries) error {
		id, err := strconv.ParseUint(s.SeriesID.String(), 10, 64)
		if err != nil || id == 0 || strings.TrimSpace(s.Name) == "" {
			return nil
		}
		stubs = append(stubs, SeriesStub{
			UpstreamID: id,
			Name:       s.Name,
			Group:      seriesNames[s.CategoryID.String()],
			Cover:      s.Cover,
		})
		return nil
	}); err != nil {
		return fmt.Errorf("get_series: %w", err)
	}
	logger.Default.Logf("xtream preamble %s: %d live, %d vod, %d series lists fetched in %.1fs", c.Host, liveCount, vodCount, len(stubs), time.Since(started).Seconds())

	if cache == nil {
		return nil
	}
	if err := WriteSeriesStubs(cache.Stubs, stubs); err != nil {
		logger.Default.Warnf("Xtream series stub write failed: %v", err)
	}

	entries, err := ReadSeriesFragment(cache.Frag)
	if err != nil {
		return nil
	}
	byID := make(map[uint64][]string, len(entries))
	for _, e := range entries {
		byID[e.UpstreamID] = e.Lines
	}
	replayed := 0
	for _, stub := range stubs {
		for _, line := range byID[stub.UpstreamID] {
			if err := emit(line); err != nil {
				return err
			}
			replayed++
		}
	}
	if replayed > 0 {
		logger.Default.Logf("Xtream: replayed %d cached series lines", replayed)
	}
	return nil
}

// escapeAttr escapes a value for use inside a double-quoted M3U attribute.
func escapeAttr(v string) string {
	r := strings.NewReplacer(`"`, `'`, "\n", " ", "\r", " ")
	return r.Replace(v)
}
