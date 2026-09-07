package xtream

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"m3u-stream-merger/logger"
	"m3u-stream-merger/utils"

	"github.com/goccy/go-json"
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

// fetchOnce performs one API attempt; retryable marks truncated 200s and 5xx.
func fetchOnce[T any](ctx context.Context, c *Client, action string, extra url.Values) (*T, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiURL(action, extra), nil)
	if err != nil {
		return nil, false, err
	}

	resp, err := utils.HTTPClient.Do(req)
	if err != nil {
		return nil, false, err
	}
	if resp.StatusCode >= 500 {
		_ = resp.Body.Close()
		return nil, true, fmt.Errorf("xtream api %s returned status %d", action, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, false, fmt.Errorf("xtream api %s returned status %d", action, resp.StatusCode)
	}

	var result T
	decodeErr := json.NewDecoder(resp.Body).Decode(&result)
	_ = resp.Body.Close()
	if decodeErr != nil {
		return nil, true, fmt.Errorf("xtream api %s decode error: %w", action, decodeErr)
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
	result, err := fetchAPI[[]T](ctx, c, action, nil)
	if err != nil {
		return nil, err
	}
	return *result, nil
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
	return fetchList[RawLiveStream](ctx, c, "get_live_streams")
}

func (c *Client) VodStreams(ctx context.Context) ([]RawVodStream, error) {
	return fetchList[RawVodStream](ctx, c, "get_vod_streams")
}

func (c *Client) SeriesList(ctx context.Context) ([]RawSeries, error) {
	return fetchList[RawSeries](ctx, c, "get_series")
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
		if _, ok := m[cat.CategoryID.String()]; !ok {
			m[cat.CategoryID.String()] = cat.CategoryName
		}
	}
	return m
}

const seriesInfoWorkers = 8

// seriesFailLimit aborts the per-series fetch loop when the panel is failing everything.
const seriesFailLimit = 25

func xtreamSeriesWorkers() int {
	if v := os.Getenv("XTREAM_SERIES_WORKERS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return min(n, 64)
		}
	}
	return seriesInfoWorkers
}

// FetchPlaylistLines fetches the catalog concurrently and emits M3U lines in stable
// section order (live, vod, series); onSeriesProgress receives (done, total) counts.
func FetchPlaylistLines(ctx context.Context, c *Client, emit func(line string) error, onSeriesProgress func(done, total int)) error {
	var (
		liveCats, vodCats, seriesCats []RawCategory
		live                          []RawLiveStream
		vod                           []RawVodStream
		seriesList                    []RawSeries
		liveErr, vodErr, seriesErr    error
		fetchWg                       sync.WaitGroup
	)
	fetchWg.Go(func() { liveCats, _ = c.LiveCategories(ctx) })
	fetchWg.Go(func() { vodCats, _ = c.VodCategories(ctx) })
	fetchWg.Go(func() { seriesCats, _ = c.SeriesCategories(ctx) })
	fetchWg.Go(func() { live, liveErr = c.LiveStreams(ctx) })
	fetchWg.Go(func() { vod, vodErr = c.VodStreams(ctx) })
	fetchWg.Go(func() { seriesList, seriesErr = c.SeriesList(ctx) })
	fetchWg.Wait()

	if liveErr != nil {
		return fmt.Errorf("get_live_streams: %w", liveErr)
	}
	if vodErr != nil {
		return fmt.Errorf("get_vod_streams: %w", vodErr)
	}
	if seriesErr != nil {
		return fmt.Errorf("get_series: %w", seriesErr)
	}

	liveNames := categoryMap(liveCats)
	vodNames := categoryMap(vodCats)
	seriesNames := categoryMap(seriesCats)

	writeEntry := func(title, group, tvgType, logo, tvgID, streamURL string) error {
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
		if err := emit(fmt.Sprintf("#EXTINF:-1 %s,%s", attrs, title)); err != nil {
			return err
		}
		return emit(streamURL)
	}

	for _, s := range live {
		streamURL := fmt.Sprintf("%s/live/%s/%s/%s.ts", c.Host, c.Username, c.Password, s.StreamID.String())
		if err := writeEntry(s.Name, liveNames[s.CategoryID.String()], "live", s.StreamIcon, s.EPGChannelID, streamURL); err != nil {
			return err
		}
	}

	for _, s := range vod {
		ext := s.ContainerExtension
		if ext == "" {
			ext = "mp4"
		}
		streamURL := fmt.Sprintf("%s/movie/%s/%s/%s.%s", c.Host, c.Username, c.Password, s.StreamID.String(), ext)
		if err := writeEntry(s.Name, vodNames[s.CategoryID.String()], "movie", s.StreamIcon, "", streamURL); err != nil {
			return err
		}
	}

	logger.Default.Logf("Xtream: fetching info for %d series", len(seriesList))

	var (
		emitMu sync.Mutex
		first  error
	)
	setError := func(err error) {
		emitMu.Lock()
		defer emitMu.Unlock()
		if first == nil {
			first = err
		}
	}

	jobs := make(chan RawSeries)
	var wg sync.WaitGroup
	var failStreak atomic.Int32
	var seriesDone atomic.Int64
	total := len(seriesList)
	reportProgress := func() {
		if onSeriesProgress != nil {
			onSeriesProgress(int(seriesDone.Load()), total)
		}
	}
	for range xtreamSeriesWorkers() {
		wg.Go(func() {
			for series := range jobs {
				if failStreak.Load() >= seriesFailLimit {
					continue
				}
				info, err := c.SeriesInfo(ctx, series.SeriesID.String())
				if n := seriesDone.Add(1); n%1000 == 0 {
					reportProgress()
				}
				if err != nil {
					if n := failStreak.Add(1); n == seriesFailLimit {
						logger.Default.Warnf("Xtream get_series_info failed %d times in a row, skipping remaining series", seriesFailLimit)
					} else if n < seriesFailLimit {
						logger.Default.Warnf("xtream get_series_info %s (%s): %v", series.SeriesID.String(), series.Name, err)
					}
					continue
				}
				failStreak.Store(0)
				for seasonNum, episodes := range info.Episodes {
					for _, ep := range episodes {
						epNum := ep.EpisodeNum
						if epNum == 0 {
							epNum, _ = strconv.Atoi(ep.ID.String())
						}
						title := fmt.Sprintf("%s S%sE%d", series.Name, strings.TrimPrefix(seasonNum, "0"), epNum)
						ext := ep.ContainerExtension
						if ext == "" {
							ext = "mkv"
						}
						streamURL := fmt.Sprintf("%s/series/%s/%s/%s.%s", c.Host, c.Username, c.Password, ep.ID.String(), ext)
						emitMu.Lock()
						err := writeEntry(title, seriesNames[series.CategoryID.String()], "series", ep.MovieImage, "", streamURL)
						emitMu.Unlock()
						if err != nil {
							setError(err)
							return
						}
					}
				}
			}
		})
	}
	for _, series := range seriesList {
		select {
		case jobs <- series:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return ctx.Err()
		}
	}
	close(jobs)
	wg.Wait()
	reportProgress()

	return first
}

// escapeAttr escapes a value for use inside a double-quoted M3U attribute.
func escapeAttr(v string) string {
	r := strings.NewReplacer(`"`, `'`, "\n", " ", "\r", " ")
	return r.Replace(v)
}
