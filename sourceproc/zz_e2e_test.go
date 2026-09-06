package sourceproc

import (
	"bufio"
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// writeCorpus writes n M3U entries; every overlapEvery-th entry reuses a source-A title to exercise the merge path.
func writeCorpus(tb testing.TB, path string, n, offset, overlapEvery int) {
	tb.Helper()
	f, err := os.Create(path)
	if err != nil {
		tb.Fatal(err)
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 1<<20)
	fmt.Fprint(w, "#EXTM3U\n")
	for i := range n {
		id := i + offset
		if overlapEvery > 0 && i%overlapEvery == 0 {
			id = i
		}
		fmt.Fprintf(w, "#EXTINF:-1 tvg-id=\"chan.%d.tv\" tvg-chno=\"%d\" tvg-name=\"Channel %d HD\" tvg-type=\"live\" tvg-logo=\"http://img.example.com/%d.png\" tvg-group=\"Group %d\" group-title=\"Group %d\",Channel %d HD\n", id, id, id, id, id%50, id%50, id)
		fmt.Fprintf(w, "http://upstream.example.com/live/src%d/%d-%d.m3u8\n", offset, id, i)
	}
	if err := w.Flush(); err != nil {
		tb.Fatal(err)
	}
}

func BenchmarkEndToEndIngest(b *testing.B) {
	benchDataDir(b)
	dir := b.TempDir()
	const perSource = 250_000

	aPath := filepath.Join(dir, "a.m3u")
	bPath := filepath.Join(dir, "b.m3u")
	writeCorpus(b, aPath, perSource, 0, 0)
	writeCorpus(b, bPath, perSource, 1_000_000, 5)

	b.Setenv("M3U_URL_1", "file://"+aPath)
	b.Setenv("M3U_URL_2", "file://"+bPath)
	b.Setenv("INCLUDE_GROUPS", "^Group")

	cases := []struct{ key string }{{""}, {"tvg-name"}}
	for _, tc := range cases {
		b.Run("sort="+tc.key, func(b *testing.B) {
			if tc.key == "" {
				os.Unsetenv("SORTING_KEY")
			} else {
				os.Setenv("SORTING_KEY", tc.key)
				defer os.Unsetenv("SORTING_KEY")
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				p := NewProcessor()
				if p == nil {
					b.Fatal("nil processor")
				}
				req := httptest.NewRequest("GET", "http://bench.local/", nil)
				if err := p.Run(context.Background(), req); err != nil {
					b.Fatal(err)
				}
				if p.GetCount() != 2*perSource {
					b.Fatalf("ingested %d streams, want %d", p.GetCount(), 2*perSource)
				}
			}
		})
	}
}
