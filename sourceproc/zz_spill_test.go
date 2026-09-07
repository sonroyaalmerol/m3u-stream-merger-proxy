package sourceproc

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"m3u-stream-merger/config"
)

func TestStreamInfoCodecRoundTrip(t *testing.T) {
	want := &StreamInfo{
		Title:       "Chan \u00e9\u00f1 1",
		TvgID:       "id.1",
		TvgChNo:     "42",
		TvgType:     "live",
		LogoURL:     "http://logo/x.png",
		Group:       "News",
		SourceM3U:   "2",
		SourceIndex: 7,
		URLs: []StreamURL{
			{M3UIndex: "1", LineNum: 3, URL: "http://a/1.ts"},
			{M3UIndex: "2", LineNum: 900001, URL: ""},
		},
	}

	rec := appendStreamInfo(nil, want)
	var got StreamInfo
	if err := decodeStreamInfo(rec, &got); err != nil {
		t.Fatal("decode:", err)
	}
	if !reflect.DeepEqual(want, &got) {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, want)
	}

	for n := range len(rec) {
		if err := decodeStreamInfo(rec[:n], new(StreamInfo)); err == nil {
			t.Fatalf("truncation at %d decoded without error", n)
		}
	}

	var empty StreamInfo
	if err := decodeStreamInfo(appendStreamInfo(nil, &empty), &empty); err != nil {
		t.Fatal("empty decode:", err)
	}
	if empty.URLs != nil {
		t.Fatalf("empty URLs decoded as %v", empty.URLs)
	}
}

func TestAppendSanitizedMatchesSanitizeField(t *testing.T) {
	for _, value := range []string{
		"",
		"normal",
		`a b/c\d:e*f?g"h<i>j|k`,
		strings.Repeat("\u00e9", 101),
		strings.Repeat(" ", 120) + "kept",
		strings.Repeat(string([]byte{0xff}), 120) + "x",
	} {
		if got, want := string(appendSanitized(nil, value)), sanitizeField(value); got != want {
			t.Fatalf("sanitize mismatch: got %q, want %q", got, want)
		}
	}
}

func TestSpillSorterOrderAndFold(t *testing.T) {
	t.Setenv("SORTING_KEY", "title")
	config.SetConfig(&config.Config{DataPath: t.TempDir() + "/data/", TempPath: t.TempDir() + "/tmp/"})
	s := newSpillSorter()
	for i := range 500 {
		st := &StreamInfo{Title: fmt.Sprintf("Chan %03d", i%250), TvgID: fmt.Sprintf("id%d", i%250)}
		st.AddURL(fmt.Sprintf("%d", i%3), i, fmt.Sprintf("http://x/%d.ts", i))
		if err := s.Add(st); err != nil {
			t.Fatal("add:", err)
		}
	}

	n := 0
	prev := ""
	urls := map[string]int{}
	err := s.MergeRendered(func(e *StreamInfo, rb *renderBuf) renderedEntry {
		rb.m3u.Reset()
		rb.m3u.WriteString(e.Title)
		if len(e.URLs) == 0 {
			t.Errorf("entry %q lost its URLs through the spill codec", e.Title)
		}
		return renderedEntry{storeKey: 1, m3u: rb.m3u.Bytes(), storeRec: []byte("x"), tvgID: []byte(e.TvgID)}
	}, func(re renderedEntry) error {
		m3u := string(re.m3u)
		if m3u == prev {
			t.Errorf("duplicate entry emitted: %q", m3u)
		}
		if m3u < prev {
			t.Fatalf("out of order: %q after %q", m3u, prev)
		}
		prev = m3u
		urls[m3u]++
		n++
		return nil
	})
	if err != nil {
		t.Fatal("merge:", err)
	}
	if n != 250 {
		t.Fatalf("got %d folded entries, want 250", n)
	}
	s.Close()
}

func TestSpillSorterProviderOrderDefault(t *testing.T) {
	config.SetConfig(&config.Config{DataPath: t.TempDir() + "/data/", TempPath: t.TempDir() + "/tmp/"})
	s := newSpillSorter()
	if s.sortingKey != "provider-order" {
		t.Fatalf("default SORTING_KEY = %q, want provider-order", s.sortingKey)
	}
	s.Close()
}

func TestSpillSorterProviderOrder(t *testing.T) {
	t.Setenv("SORTING_KEY", "provider-order")
	config.SetConfig(&config.Config{DataPath: t.TempDir() + "/data/", TempPath: t.TempDir() + "/tmp/"})
	s := newSpillSorter()

	for _, st := range []*StreamInfo{
		{Title: "A", SourceM3U: "2", SourceIndex: 5},
		{Title: "B", SourceM3U: "1", SourceIndex: 40},
		{Title: "C", SourceM3U: "2", SourceIndex: 60},
		{Title: "D", SourceM3U: "2", SourceIndex: 70},
		{Title: "D", SourceM3U: "1", SourceIndex: 10},
		{Title: "E", SourceM3U: "1", SourceIndex: 1000},
	} {
		if err := s.Add(st); err != nil {
			t.Fatal("add:", err)
		}
	}

	var got []string
	err := s.MergeRendered(func(e *StreamInfo, rb *renderBuf) renderedEntry {
		rb.m3u.Reset()
		rb.m3u.WriteString(e.Title)
		return renderedEntry{m3u: rb.m3u.Bytes()}
	}, func(re renderedEntry) error {
		got = append(got, string(re.m3u))
		return nil
	})
	if err != nil {
		t.Fatal("merge:", err)
	}
	want := []string{"D", "B", "E", "A", "C"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("provider order: got %v, want %v", got, want)
	}
	s.Close()
}

func TestSpillSorterDescNumeric(t *testing.T) {
	t.Setenv("SORTING_KEY", "tvg-chno")
	t.Setenv("SORTING_DIRECTION", "desc")
	config.SetConfig(&config.Config{DataPath: t.TempDir() + "/data/", TempPath: t.TempDir() + "/tmp/"})
	s := newSpillSorter()
	for i := range 50 {
		st := &StreamInfo{Title: fmt.Sprintf("Chan %d", i), TvgChNo: fmt.Sprintf("%d", i%10)}
		st.AddURL("1", i, fmt.Sprintf("http://x/%d.ts", i))
		if err := s.Add(st); err != nil {
			t.Fatal("add:", err)
		}
	}

	var got []int
	err := s.MergeRendered(func(e *StreamInfo, rb *renderBuf) renderedEntry {
		rb.m3u.Reset()
		rb.m3u.WriteString(e.TvgChNo)
		return renderedEntry{m3u: rb.m3u.Bytes()}
	}, func(re renderedEntry) error {
		var v int
		fmt.Sscanf(string(re.m3u), "%d", &v)
		got = append(got, v)
		return nil
	})
	if err != nil {
		t.Fatal("merge:", err)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1] < got[i] {
			t.Fatalf("not desc-numeric: %v", got)
		}
	}
	s.Close()
}
