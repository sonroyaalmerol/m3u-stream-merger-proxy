package sourceproc

import (
	"fmt"
	"testing"

	"m3u-stream-merger/config"
)

func TestSpillSorterOrderAndFold(t *testing.T) {
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
	err := s.MergeRendered(func(e *StreamInfo) renderedEntry {
		return renderedEntry{storeKey: 1, m3u: e.Title, storeRec: []byte("x"), tvgID: e.TvgID}
	}, func(re renderedEntry) error {
		if re.m3u == prev {
			t.Errorf("duplicate entry emitted: %q", re.m3u)
		}
		if re.m3u < prev {
			t.Fatalf("out of order: %q after %q", re.m3u, prev)
		}
		prev = re.m3u
		urls[re.m3u]++
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
	err := s.MergeRendered(func(e *StreamInfo) renderedEntry {
		return renderedEntry{m3u: e.TvgChNo}
	}, func(re renderedEntry) error {
		var v int
		fmt.Sscanf(re.m3u, "%d", &v)
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
