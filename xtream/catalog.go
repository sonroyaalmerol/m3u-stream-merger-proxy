package xtream

import (
	"bufio"
	"encoding/json"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/cespare/xxhash"
)

const (
	TypeLive   = "live"
	TypeMovie  = "movie"
	TypeSeries = "series"
)

type Entry struct {
	StreamID uint64
	Title    string
	Show     string
	Season   int
	Episode  int
	TvgID    string
	Group    string
	Logo     string
	Type     string
	Slug     string
	BasePath string
	Ext      string
}

type SeriesData struct {
	SeriesID uint64
	Name     string
	Group    string
	Cover    string
	Episodes map[int][]*Entry
}

type Catalog struct {
	mu       sync.RWMutex
	live     []*Entry
	vod      []*Entry
	series   map[uint64]*SeriesData
	seriesLv []*SeriesData
	cats     map[string][]RawCategory
	catIDs   map[string]uint64
	byID     map[uint64]*Entry
}

func NewCatalog() *Catalog {
	return &Catalog{}
}

var globalCatalog = NewCatalog()

func GetCatalog() *Catalog {
	return globalCatalog
}

var (
	extinfAttrRegex = regexp.MustCompile(`([a-zA-Z0-9_-]+)="([^"]*)"`)
	episodeRegex    = regexp.MustCompile(`^(.*?)\s+[sS](\d{1,4})[eE](\d{1,4})$`)
)

func streamID(title string) uint64 { return xxhash.Sum64String(title) }
func seriesID(show string) uint64  { return xxhash.Sum64String("series|" + show) }
func categoryID(key string) uint64 { return xxhash.Sum64String("cat|"+key) & 0x7FFFFFFF }

// Rebuild parses the merged M3U and atomically swaps the catalog contents;
// regexp-heavy entry parsing fans out across cores behind a file-order reorder buffer.
func (c *Catalog) Rebuild(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	newC := &Catalog{
		series: make(map[uint64]*SeriesData),
		cats:   make(map[string][]RawCategory),
		catIDs: make(map[string]uint64),
		byID:   make(map[uint64]*Entry),
	}

	type rawPair struct {
		seq    int
		extinf string
		url    string
	}
	type parsedPair struct {
		seq   int
		entry *Entry
	}
	pairs := make(chan rawPair, 4096)
	parsed := make(chan parsedPair, 4096)

	var scanErr error
	go func() {
		defer close(pairs)
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)

		seq := 0
		var pending string
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if strings.HasPrefix(line, "#EXTINF:") {
				pending = line
				continue
			}
			if pending == "" || strings.HasPrefix(line, "#") || !strings.HasPrefix(line, "http") {
				pending = ""
				continue
			}
			pairs <- rawPair{seq: seq, extinf: pending, url: line}
			seq++
			pending = ""
		}
		scanErr = scanner.Err()
	}()

	var pwg sync.WaitGroup
	for range max(1, runtime.GOMAXPROCS(0)) {
		pwg.Go(func() {
			for p := range pairs {
				parsed <- parsedPair{seq: p.seq, entry: parseEntry(p.extinf, p.url)}
			}
		})
	}
	go func() {
		pwg.Wait()
		close(parsed)
	}()

	reorder := make(map[int]*Entry)
	next := 0
	for res := range parsed {
		reorder[res.seq] = res.entry
		for {
			e, ok := reorder[next]
			if !ok {
				break
			}
			delete(reorder, next)
			next++
			if e != nil {
				newC.add(e)
				newC.addCategory(e)
			}
		}
	}
	if scanErr != nil {
		return scanErr
	}

	for _, list := range newC.cats {
		sort.Slice(list, func(i, j int) bool {
			return list[i].CategoryName < list[j].CategoryName
		})
	}
	newC.live = sortEntries(newC.live)
	newC.vod = sortEntries(newC.vod)
	sort.Slice(newC.seriesLv, func(i, j int) bool {
		return newC.seriesLv[i].Name < newC.seriesLv[j].Name
	})
	for _, sd := range newC.seriesLv {
		for season := range sd.Episodes {
			sort.Slice(sd.Episodes[season], func(i, j int) bool {
				return sd.Episodes[season][i].Episode < sd.Episodes[season][j].Episode
			})
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.live, c.vod, c.series, c.seriesLv = newC.live, newC.vod, newC.series, newC.seriesLv
	c.cats, c.catIDs, c.byID = newC.cats, newC.catIDs, newC.byID
	return nil
}

func sortEntries(entries []*Entry) []*Entry {
	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Title) < strings.ToLower(entries[j].Title)
	})
	return entries
}

func parseEntry(extinf, url string) *Entry {
	e := &Entry{Title: extinf}

	for _, match := range extinfAttrRegex.FindAllStringSubmatch(extinf, -1) {
		switch strings.ToLower(match[1]) {
		case "tvg-name":
			e.Title = match[2]
		case "tvg-id":
			e.TvgID = match[2]
		case "tvg-logo":
			e.Logo = match[2]
		case "tvg-group", "group-title":
			e.Group = match[2]
		case "tvg-type":
			e.Type = strings.ToLower(match[2])
		}
	}
	if comma := strings.SplitN(extinf, ",", 2); len(comma) == 2 {
		e.Title = strings.TrimSpace(comma[1])
	}
	if e.Title == "" {
		return nil
	}

	e.Type = normalizeType(e.Type, url)
	e.BasePath, e.Slug, e.Ext = parseProxyURL(url)

	if m := episodeRegex.FindStringSubmatch(e.Title); m != nil {
		e.Show = strings.TrimSpace(m[1])
		e.Season, _ = strconv.Atoi(m[2])
		e.Episode, _ = strconv.Atoi(m[3])
	}

	e.StreamID = streamID(e.Title)
	return e
}

func normalizeType(tvgType, url string) string {
	switch tvgType {
	case TypeLive, TypeMovie, TypeSeries:
		return tvgType
	}
	if _, after, ok := strings.Cut(url, "/p/"); ok {
		rest := after
		if slash := strings.Index(rest, "/"); slash > 0 {
			switch rest[:slash] {
			case TypeLive:
				return TypeLive
			case TypeMovie:
				return TypeMovie
			case TypeSeries:
				return TypeSeries
			}
		}
	}
	return TypeLive
}

// parseProxyURL splits {base}/p/{basePath}/{slug}.{ext} produced by the merger.
func parseProxyURL(url string) (basePath, slug, ext string) {
	_, after, ok := strings.Cut(url, "/p/")
	if !ok {
		return "stream", "", ""
	}
	rest := after
	slash := strings.LastIndex(rest, "/")
	if slash < 0 {
		return "stream", trimExt(rest), ""
	}
	basePath = rest[:slash]
	file := rest[slash+1:]
	dot := strings.LastIndex(file, ".")
	if dot < 0 {
		return basePath, file, ""
	}
	slug, ext = file[:dot], file[dot:]
	if ext == ".m3u" || ext == ".m3u8" {
		ext = ""
	}
	return basePath, slug, ext
}

func trimExt(s string) string {
	if dot := strings.LastIndex(s, "."); dot >= 0 {
		return s[:dot]
	}
	return s
}

func (c *Catalog) add(e *Entry) {
	c.byID[e.StreamID] = e
	switch {
	case e.Type == TypeMovie:
		c.vod = append(c.vod, e)
	case e.Type == TypeSeries && e.Show != "":
		sid := seriesID(e.Show)
		sd, ok := c.series[sid]
		if !ok {
			sd = &SeriesData{SeriesID: sid, Name: e.Show, Group: e.Group, Episodes: make(map[int][]*Entry)}
			c.series[sid] = sd
			c.seriesLv = append(c.seriesLv, sd)
		}
		if sd.Cover == "" {
			sd.Cover = e.Logo
		}
		sd.Episodes[e.Season] = append(sd.Episodes[e.Season], e)
	default:
		c.live = append(c.live, e)
	}
}

func (c *Catalog) addCategory(e *Entry) {
	if e.Group == "" {
		return
	}
	if c.cats[e.Type] == nil {
		c.cats[e.Type] = []RawCategory{}
	}
	for _, existing := range c.cats[e.Type] {
		if existing.CategoryName == e.Group {
			return
		}
	}
	id := categoryID(e.Type + "|" + e.Group)
	c.catIDs[e.Type+"|"+e.Group] = id
	c.cats[e.Type] = append(c.cats[e.Type], RawCategory{
		CategoryID:   jsonNumber(id),
		CategoryName: e.Group,
		ParentID:     jsonNumber(0),
	})
}

func (c *Catalog) Categories(streamType string) []RawCategory {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]RawCategory(nil), c.cats[streamType]...)
}

func (c *Catalog) CategoryID(streamType, group string) uint64 {
	if group == "" {
		return 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.catIDs[streamType+"|"+group]
}

func (c *Catalog) Live(categoryID uint64) []*Entry {
	return filterEntries(c.live, categoryID, c.catIDs)
}

func (c *Catalog) Vod(categoryID uint64) []*Entry {
	return filterEntries(c.vod, categoryID, c.catIDs)
}

func (c *Catalog) SeriesList(categoryID uint64) []*SeriesData {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if categoryID == 0 {
		return append([]*SeriesData(nil), c.seriesLv...)
	}
	filtered := make([]*SeriesData, 0, len(c.seriesLv))
	for _, sd := range c.seriesLv {
		if c.catIDs[TypeSeries+"|"+sd.Group] == categoryID {
			filtered = append(filtered, sd)
		}
	}
	return filtered
}

func filterEntries(entries []*Entry, categoryID uint64, catIDs map[string]uint64) []*Entry {
	if categoryID == 0 {
		return append([]*Entry(nil), entries...)
	}
	filtered := make([]*Entry, 0, len(entries))
	for _, e := range entries {
		if catIDs[e.Type+"|"+e.Group] == categoryID {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

func (c *Catalog) SeriesInfo(id uint64) *SeriesData {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.series[id]
}

func (c *Catalog) FindStream(id uint64) *Entry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if e, ok := c.byID[id]; ok {
		return e
	}
	return nil
}

func jsonNumber(v uint64) json.Number {
	return json.Number(strconv.FormatUint(v, 10))
}
