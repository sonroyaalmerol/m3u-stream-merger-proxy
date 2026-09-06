package xtream

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

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

func fetchAPI[T any](ctx context.Context, c *Client, action string, extra url.Values) (*T, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiURL(action, extra), nil)
	if err != nil {
		return nil, err
	}

	resp, err := utils.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("xtream api %s returned status %d", action, resp.StatusCode)
	}

	var result T
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("xtream api %s decode error: %w", action, err)
	}

	return &result, nil
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
	return fetchAPI[RawSeriesInfo](ctx, c, "get_series_info", url.Values{"series_id": {seriesID}})
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

// FetchPlaylistLines pulls the full account catalog from the panel and emits
// synthesized M3U lines through emit. Line order between live, vod and series
// sections is stable but episode order within series is not (concurrent fetch).
// ponytail: fixed 8-worker pool keeps per-series get_series_info calls polite;
// make it configurable if large panels need faster syncs.
func FetchPlaylistLines(ctx context.Context, c *Client, emit func(line string) error) error {
	liveCats, _ := c.LiveCategories(ctx)
	vodCats, _ := c.VodCategories(ctx)
	seriesCats, _ := c.SeriesCategories(ctx)

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

	live, err := c.LiveStreams(ctx)
	if err != nil {
		return fmt.Errorf("get_live_streams: %w", err)
	}
	for _, s := range live {
		streamURL := fmt.Sprintf("%s/live/%s/%s/%s.ts", c.Host, c.Username, c.Password, s.StreamID.String())
		if err := writeEntry(s.Name, liveNames[s.CategoryID.String()], "live", s.StreamIcon, s.EPGChannelID, streamURL); err != nil {
			return err
		}
	}

	vod, err := c.VodStreams(ctx)
	if err != nil {
		return fmt.Errorf("get_vod_streams: %w", err)
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

	seriesList, err := c.SeriesList(ctx)
	if err != nil {
		return fmt.Errorf("get_series: %w", err)
	}

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
	for range seriesInfoWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for series := range jobs {
				info, err := c.SeriesInfo(ctx, series.SeriesID.String())
				if err != nil {
					logger.Default.Warnf("xtream get_series_info %s (%s): %v", series.SeriesID.String(), series.Name, err)
					continue
				}
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
		}()
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

	return first
}

// escapeAttr escapes a value for use inside a double-quoted M3U attribute.
func escapeAttr(v string) string {
	r := strings.NewReplacer(`"`, `'`, "\n", " ", "\r", " ")
	return r.Replace(v)
}
