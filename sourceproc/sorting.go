package sourceproc

import (
	"cmp"
	"os"
	"slices"
	"strconv"
	"strings"

	"m3u-stream-merger/config"

	"github.com/puzpuzpuz/xsync/v3"
)

type SortingManager struct {
	streams    *xsync.MapOf[string, *StreamInfo]
	sortingKey string
	sortingDir string
}

type sortEntry struct {
	stream *StreamInfo
	key    string
	num    int
	numOK  bool
}

func newSortingManager() *SortingManager {
	_ = os.RemoveAll(config.GetSortDirPath())

	return &SortingManager{
		streams:    xsync.NewMapOf[string, *StreamInfo](),
		sortingKey: os.Getenv("SORTING_KEY"),
		sortingDir: strings.ToLower(os.Getenv("SORTING_DIRECTION")),
	}
}

func (m *SortingManager) AddToSorter(s *StreamInfo) error {
	m.streams.Compute(sanitizeField(s.Title), func(old *StreamInfo, loaded bool) (*StreamInfo, bool) {
		if !loaded {
			return s, false
		}
		return mergeStreamInfoAttributes(old, s), false
	})

	return nil
}

func (m *SortingManager) Close() {
	m.streams.Clear()
}

func (m *SortingManager) GetSortedEntries(callback func(*StreamInfo)) error {
	entries := make([]sortEntry, 0, m.streams.Size())
	m.streams.Range(func(_ string, s *StreamInfo) bool {
		entries = append(entries, m.sortEntryFor(s))
		return true
	})

	desc := m.sortingDir == "desc"
	slices.SortFunc(entries, func(a, b sortEntry) int {
		c := compareSortEntries(a, b)
		if desc {
			return -c
		}
		return c
	})

	for i := range entries {
		callback(entries[i].stream)
	}

	return nil
}

func (m *SortingManager) sortEntryFor(s *StreamInfo) sortEntry {
	e := sortEntry{stream: s}
	numeric := false

	switch m.sortingKey {
	case "tvg-chno", "channel-id", "channel-number":
		e.key, numeric = s.TvgChNo, true
	case "tvg-id":
		e.key, numeric = s.TvgID, true
	case "source":
		e.key, numeric = s.SourceM3U, true
	case "tvg-group", "group-title":
		e.key = strings.ToLower(s.Group)
	case "tvg-type":
		e.key = strings.ToLower(s.TvgType)
	default:
		e.key = strings.ToLower(s.Title)
	}

	if numeric {
		if n, err := strconv.Atoi(e.key); err == nil {
			e.num, e.numOK = n, true
		}
	}

	return e
}

func compareSortEntries(a, b sortEntry) int {
	if a.numOK && b.numOK {
		if c := cmp.Compare(a.num, b.num); c != 0 {
			return c
		}
	} else if c := strings.Compare(a.key, b.key); c != 0 {
		return c
	}

	return strings.Compare(a.stream.Title, b.stream.Title)
}

func mergeStreamInfoAttributes(base, new *StreamInfo) *StreamInfo {
	if base.Title == "" {
		base.Title = new.Title
	}
	if base.TvgID == "" {
		base.TvgID = new.TvgID
	}
	if base.TvgChNo == "" {
		base.TvgChNo = new.TvgChNo
	}
	if base.TvgType == "" {
		base.TvgType = new.TvgType
	}
	if base.LogoURL == "" {
		base.LogoURL = new.LogoURL
	}
	if base.Group == "" {
		base.Group = new.Group
	}

	for _, u := range new.URLs {
		base.AddURL(u.M3UIndex, u.LineNum, u.URL)
	}

	if new.SourceM3U < base.SourceM3U || (new.SourceM3U == base.SourceM3U && new.SourceIndex < base.SourceIndex) {
		base.SourceM3U = new.SourceM3U
		base.SourceIndex = new.SourceIndex
	}

	return base
}

// fieldSanitizer is built once; strings.NewReplacer costs ~7KB per construction.
var fieldSanitizer = strings.NewReplacer(
	"/", "_",
	"\\", "_",
	":", "_",
	"*", "_",
	"?", "_",
	"\"", "_",
	"<", "_",
	">", "_",
	"|", "_",
	" ", "",
)

const maxFieldRunes = 100

func sanitizeField(value string) string {
	sanitized := fieldSanitizer.Replace(value)

	if len(sanitized) <= maxFieldRunes {
		return sanitized
	}

	runes := []rune(sanitized)
	if len(runes) > maxFieldRunes {
		sanitized = string(runes[:maxFieldRunes])
	}

	return sanitized
}
