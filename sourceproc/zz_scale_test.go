package sourceproc

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestGoldenOutput(t *testing.T) {
	out := os.Getenv("GOLDEN_OUT")
	if out == "" {
		t.Skip("set GOLDEN_OUT")
	}

	var golden []byte
	for _, key := range []string{"", "tvg-chno", "tvg-id", "tvg-group", "tvg-type", "source"} {
		for _, dir := range []string{"asc", "desc"} {
			cleanup := setupTestEnvironment(t)
			t.Setenv("SORTING_KEY", key)
			t.Setenv("SORTING_DIRECTION", dir)

			processor := NewProcessor()
			req := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := processor.Run(ctx, req); err != nil {
				t.Fatal(err)
			}
			cancel()

			content, err := os.ReadFile(processor.GetResultPath())
			if err != nil {
				t.Fatal(err)
			}
			golden = append(golden, fmt.Sprintf("=== key=%q dir=%q\n", key, dir)...)
			golden = append(golden, content...)
			cleanup()
		}
	}

	if err := os.WriteFile(out, golden, 0644); err != nil {
		t.Fatal(err)
	}
	fmt.Println("wrote", out, len(golden), "bytes")
}

func TestEndToEndSlugLookup(t *testing.T) {
	cleanup := setupTestEnvironment(t)
	defer cleanup()

	processor := NewProcessor()
	req := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := processor.Run(ctx, req); err != nil {
		t.Fatal(err)
	}
	cancel()

	content, err := os.ReadFile(processor.GetResultPath())
	if err != nil {
		t.Fatal(err)
	}

	found := 0
	for line := range strings.SplitSeq(string(content), "\n") {
		_, slug, ok := strings.Cut(line, "/p/stream/")
		if !ok {
			continue
		}
		found++

		info, err := ParseStreamInfoBySlug(slug)
		if err != nil {
			t.Fatalf("slug %s: %v", slug, err)
		}
		if info.Title == "" {
			t.Fatalf("slug %s resolved to an empty title", slug)
		}
		if len(info.URLs) == 0 {
			t.Fatalf("slug %s resolved without upstream URLs", slug)
		}
		for _, u := range info.URLs {
			if u.URL == "" || u.M3UIndex == "" {
				t.Fatalf("slug %s has an incomplete URL entry: %+v", slug, u)
			}
		}
	}

	if found == 0 {
		t.Fatal("no stream URLs in output")
	}
}

func BenchmarkScaleProbe(b *testing.B) {
	for _, n := range []int{100000, 400000, 800000} {
		b.Run(fmt.Sprintf("streams=%d", n), func(b *testing.B) {
			benchDataDir(b)
			b.ReportAllocs()
			for b.Loop() {
				b.StopTimer()
				streams := benchStreams(n)
				m := newSpillSorter()
				b.StartTimer()

				for _, s := range streams {
					if err := m.Add(s); err != nil {
						b.Fatal(err)
					}
				}
				count := 0
				err := m.MergeRendered(func(*StreamInfo) renderedEntry { return renderedEntry{} },
					func(renderedEntry) error { count++; return nil })
				if err != nil {
					b.Fatal(err)
				}

				b.StopTimer()
				b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(n)/float64(max(b.N, 1)), "ns/stream")
				m.Close()
				b.StartTimer()
			}
		})
	}
}
