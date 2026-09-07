package handlers

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"m3u-stream-merger/config"
	"m3u-stream-merger/sourceproc"
	"m3u-stream-merger/xtream"

	"github.com/goccy/go-json"
)

type stubSource struct {
	Idx        string
	UpstreamID uint64
}

type stubSeries struct {
	SeriesID   uint64
	CategoryID uint64
	Name       string
	Cover      string
	Group      string
	Sources    []stubSource
}

// seriesStubRegistry merges the per-source stub files written at ingest; scanned at most every 5s.
type seriesStubRegistry struct {
	mu      sync.Mutex
	byID    map[uint64]*stubSeries
	scanned time.Time
}

const stubScanInterval = 5 * time.Second

func (r *seriesStubRegistry) get() map[uint64]*stubSeries {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byID != nil && time.Since(r.scanned) < stubScanInterval {
		return r.byID
	}
	r.scanned = time.Now()
	byID := make(map[uint64]*stubSeries)
	files, _ := filepath.Glob(filepath.Join(config.GetSeriesCacheDirPath(), "stubs-*.bin"))
	for _, f := range files {
		idx := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "stubs-"), ".bin")
		stubs, err := xtream.ReadSeriesStubs(f)
		if err != nil {
			continue
		}
		for _, s := range stubs {
			id := sourceproc.SeriesIDFor(s.Name)
			if existing, ok := byID[id]; ok {
				existing.Sources = append(existing.Sources, stubSource{Idx: idx, UpstreamID: s.UpstreamID})
				continue
			}
			byID[id] = &stubSeries{
				SeriesID:   id,
				CategoryID: sourceproc.SeriesCategoryID(s.Group),
				Name:       s.Name,
				Cover:      s.Cover,
				Group:      s.Group,
				Sources:    []stubSource{{Idx: idx, UpstreamID: s.UpstreamID}},
			}
		}
	}
	r.byID = byID
	return byID
}

type lazyEpisode struct {
	srcIdx     string
	upstreamID uint64
	ext        string
}

type fetchResult struct {
	src  stubSource
	info *xtream.RawSeriesInfo
}

type lazySeries struct {
	out      *xtream.SeriesInfoOut
	episodes map[uint64]lazyEpisode
}

const lazySeriesCacheLimit = 1024

func (h *XtreamHTTPHandler) stubs() map[uint64]*stubSeries {
	return h.stubRegistry.get()
}

// lazySeriesInfo resolves a stub-only series on demand and persists it for the next ingest.
func (h *XtreamHTTPHandler) lazySeriesInfo(ctx context.Context, id uint64) *xtream.SeriesInfoOut {
	stubs := h.stubs()
	stub, ok := stubs[id]
	if !ok {
		return nil
	}

	h.lazyMu.Lock()
	if ls, ok := h.lazyCache[id]; ok {
		out := ls.out
		h.lazyMu.Unlock()
		return out
	}
	h.lazyMu.Unlock()

	results := make([]fetchResult, len(stub.Sources))
	var wg sync.WaitGroup
	for i, src := range stub.Sources {
		wg.Add(1)
		go func(i int, src stubSource) {
			defer wg.Done()
			c := xtream.NewClient(
				os.Getenv("XTREAM_URL_"+src.Idx),
				os.Getenv("XTREAM_USERNAME_"+src.Idx),
				os.Getenv("XTREAM_PASSWORD_"+src.Idx),
			)
			info, err := c.SeriesInfo(ctx, strconv.FormatUint(src.UpstreamID, 10))
			if err != nil {
				return
			}
			results[i] = fetchResult{src: src, info: info}
		}(i, src)
	}
	wg.Wait()

	out := &xtream.SeriesInfoOut{
		Seasons:  []xtream.SeasonOut{},
		Episodes: map[string][]xtream.EpisodeOut{},
	}
	out.Info.Name = stub.Name
	out.Info.Cover = stub.Cover
	out.Info.Genre = stub.Group
	out.Info.BackdropPath = []string{}
	out.Info.CategoryID = strconv.FormatUint(stub.CategoryID, 10)

	ls := &lazySeries{out: out, episodes: make(map[uint64]lazyEpisode)}
	seen := make(map[string]struct{})
	for _, res := range results {
		if res.info == nil {
			continue
		}
		seasons := make([]string, 0, len(res.info.Episodes))
		for seasonNum := range res.info.Episodes {
			seasons = append(seasons, seasonNum)
		}
		sort.Slice(seasons, func(a, b int) bool {
			sa, _ := strconv.Atoi(seasons[a])
			sb, _ := strconv.Atoi(seasons[b])
			return sa < sb
		})
		for _, seasonNum := range seasons {
			seasonKey := strings.TrimPrefix(seasonNum, "0")
			season, _ := strconv.Atoi(seasonKey)
			key := strconv.Itoa(season)
			for i, ep := range res.info.Episodes[seasonNum] {
				upstreamID, err := strconv.ParseUint(ep.ID.String(), 10, 64)
				if err != nil || upstreamID == 0 {
					continue
				}
				epNum := ep.EpisodeNum.Int()
				if epNum <= 0 {
					epNum = i + 1
				}
				dedup := fmt.Sprintf("%d:%d", season, epNum)
				if _, dup := seen[dedup]; dup {
					continue
				}
				seen[dedup] = struct{}{}
				title := xtream.EpisodeTitle(stub.Name, seasonNum, epNum)
				ext := ep.ContainerExtension
				if ext == "" {
					ext = "mkv"
				}
				ourID := sourceproc.StreamIDFor(title)
				out.Episodes[key] = append(out.Episodes[key], xtream.EpisodeOut{
					ID:                 json.Number(strconv.FormatUint(ourID, 10)),
					EpisodeNum:         epNum,
					Title:              title,
					ContainerExtension: ext,
					Added:              "0",
					Season:             season,
					Info: xtream.EpisodeInfoOut{
						MovieImage: ep.Image(),
						Season:     season,
					},
				})
				ls.episodes[ourID] = lazyEpisode{srcIdx: res.src.Idx, upstreamID: upstreamID, ext: ext}
			}
		}
	}
	for _, season := range sortedSeasonInts(out.Episodes) {
		key := strconv.Itoa(season)
		out.Seasons = append(out.Seasons, xtream.SeasonOut{
			ID:           json.Number(key),
			Name:         "Season " + key,
			SeasonNumber: season,
			EpisodeCount: len(out.Episodes[key]),
			Cover:        stub.Cover,
			CoverBig:     stub.Cover,
		})
	}

	h.rememberLazy(id, ls)
	h.persistLazyFragments(stub, results)
	return out
}

func sortedSeasonInts(episodes map[string][]xtream.EpisodeOut) []int {
	seasons := make([]int, 0, len(episodes))
	for key := range episodes {
		n, _ := strconv.Atoi(key)
		seasons = append(seasons, n)
	}
	sort.Ints(seasons)
	return seasons
}

func (h *XtreamHTTPHandler) rememberLazy(id uint64, ls *lazySeries) {
	h.lazyMu.Lock()
	defer h.lazyMu.Unlock()
	if h.lazyCache == nil {
		h.lazyCache = make(map[uint64]*lazySeries)
	}
	if _, ok := h.lazyCache[id]; !ok {
		h.lazyOrder = append(h.lazyOrder, id)
	}
	h.lazyCache[id] = ls
	if h.lazyEpisodes == nil {
		h.lazyEpisodes = make(map[uint64]lazyEpisode)
	}
	maps.Copy(h.lazyEpisodes, ls.episodes)
	for len(h.lazyOrder) > lazySeriesCacheLimit {
		evict := h.lazyOrder[0]
		h.lazyOrder = h.lazyOrder[1:]
		if old, ok := h.lazyCache[evict]; ok {
			for epID := range old.episodes {
				delete(h.lazyEpisodes, epID)
			}
			delete(h.lazyCache, evict)
		}
	}
}

func (h *XtreamHTTPHandler) persistLazyFragments(stub *stubSeries, results []fetchResult) {
	for _, res := range results {
		if res.info == nil {
			continue
		}
		c := xtream.NewClient(
			os.Getenv("XTREAM_URL_"+res.src.Idx),
			os.Getenv("XTREAM_USERNAME_"+res.src.Idx),
			os.Getenv("XTREAM_PASSWORD_"+res.src.Idx),
		)
		entry := xtream.FragmentEntry{
			UpstreamID: res.src.UpstreamID,
			Lines:      xtream.SeriesToLines(c, stub.Name, stub.Group, res.info),
		}
		frag := filepath.Join(config.GetSeriesCacheDirPath(), "frag-"+res.src.Idx+".m3u")
		err := xtream.MutateSeriesFragment(frag, func(entries []xtream.FragmentEntry) []xtream.FragmentEntry {
			replaced := false
			for i := range entries {
				if entries[i].UpstreamID == entry.UpstreamID {
					entries[i] = entry
					replaced = true
					break
				}
			}
			if !replaced {
				entries = append(entries, entry)
			}
			return entries
		})
		if err != nil {
			h.logger.Warnf("series fragment append failed for %s: %v", res.src.Idx, err)
		}
	}
}

// serveLazyEpisode redirects a not-yet-materialized episode upstream; ingest materializes it later.
func (h *XtreamHTTPHandler) serveLazyEpisode(w http.ResponseWriter, r *http.Request, id uint64, user, pass string) bool {
	h.lazyMu.Lock()
	ep, ok := h.lazyEpisodes[id]
	h.lazyMu.Unlock()
	if !ok {
		return false
	}
	host := strings.TrimSuffix(os.Getenv("XTREAM_URL_"+ep.srcIdx), "/")
	if host == "" {
		return false
	}
	upUser := os.Getenv("XTREAM_USERNAME_" + ep.srcIdx)
	upPass := os.Getenv("XTREAM_PASSWORD_" + ep.srcIdx)
	target := fmt.Sprintf("%s/series/%s/%s/%d.%s", host, upUser, upPass, ep.upstreamID, ep.ext)
	h.logger.Debugf("Lazy episode %d -> %s", id, host)
	http.Redirect(w, r, target, http.StatusFound)
	return true
}
