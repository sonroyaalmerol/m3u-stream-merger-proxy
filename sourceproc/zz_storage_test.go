package sourceproc

import (
	"fmt"
	"os"
	"testing"
)

func storeStream(i int) *StreamInfo {
	s := &StreamInfo{
		Title:       fmt.Sprintf("Some Channel Name HD %d", i),
		TvgID:       fmt.Sprintf("chan.%d.tv", i),
		TvgChNo:     fmt.Sprintf("%d", i),
		Group:       "Sports",
		LogoURL:     fmt.Sprintf("http://example.com/logo/%d.png", i),
		SourceM3U:   "1",
		SourceIndex: i,
	}
	s.AddURL("1", i, fmt.Sprintf("http://example.com/live/user/pass/%d.ts", i))

	return s
}

func buildStore(tb testing.TB, n int) {
	tb.Helper()
	w, err := NewStreamStoreWriter()
	if err != nil {
		tb.Fatal(err)
	}
	for i := range n {
		s := storeStream(i)
		key, _ := slugParts(s.Title)
		if err := w.Add(key, s); err != nil {
			tb.Fatal(err)
		}
	}
	if err := w.Commit(); err != nil {
		tb.Fatal(err)
	}
}

func BenchmarkStreamIngest(b *testing.B) {
	benchDataDir(b)
	i := 0
	var parser streamParser
	b.ReportAllocs()
	for b.Loop() {
		i++
		line := fmt.Sprintf(`#EXTINF:-1 tvg-id="chan.%d.tv" tvg-name="Chan %d" tvg-chno="%d" group-title="Sports",Chan %d`, i, i, i, i)
		next := &LineDetails{Content: fmt.Sprintf("http://example.com/live/user/pass/%d.ts", i), LineNum: i}
		_ = parser.parseLine(line, next, "1")
	}
}

func BenchmarkStreamStoreBuild(b *testing.B) {
	for _, n := range []int{100000, 800000} {
		b.Run(fmt.Sprintf("streams=%d", n), func(b *testing.B) {
			benchDataDir(b)
			b.ReportAllocs()
			for b.Loop() {
				buildStore(b, n)
			}
		})
	}
}

func BenchmarkStreamStoreLookup(b *testing.B) {
	for _, n := range []int{10000, 100000, 800000} {
		b.Run(fmt.Sprintf("streams=%d", n), func(b *testing.B) {
			benchDataDir(b)
			buildStore(b, n)
			defaultStore.mu.Lock()
			defaultStore.loaded = false
			_ = defaultStore.loadLocked()
			defaultStore.mu.Unlock()

			slugs := make([]string, 64)
			for i := range slugs {
				slugs[i] = EncodeSlug(storeStream(i * (n / len(slugs))))
			}

			i := 0
			b.ReportAllocs()
			for b.Loop() {
				if _, err := defaultStore.Get(slugs[i%len(slugs)]); err != nil {
					b.Fatal(err)
				}
				i++
			}
		})
	}
}

func TestStreamStoreRoundTrip(t *testing.T) {
	benchDataDir(t)
	buildStore(t, 5000)

	for _, i := range []int{0, 1, 2499, 4999} {
		want := storeStream(i)
		got, err := defaultStore.Get(EncodeSlug(want))
		if err != nil {
			t.Fatalf("stream %d: %v", i, err)
		}
		if got.Title != want.Title || got.TvgID != want.TvgID || len(got.URLs) != 1 {
			t.Fatalf("stream %d: got %+v", i, got)
		}
		if got.URLs[0].URL != want.URLs[0].URL || got.URLs[0].LineNum != want.URLs[0].LineNum {
			t.Fatalf("stream %d urls: got %+v want %+v", i, got.URLs[0], want.URLs[0])
		}
	}

	if _, err := defaultStore.Get(EncodeSlug(storeStream(999999))); err == nil {
		t.Fatal("expected miss for unknown slug")
	}
	if _, err := defaultStore.Get("not-a-slug"); err == nil {
		t.Fatal("expected error for malformed slug")
	}
}

func TestStreamStoreRejectsOldIndex(t *testing.T) {
	benchDataDir(t)
	buildStore(t, 100)
	defaultStore.reset()

	file, err := os.OpenFile(indexPath(1), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("M3USTR02"), 0); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := defaultStore.Get(EncodeSlug(storeStream(50))); err == nil {
		t.Fatal("expected old catalog format to be rejected")
	}
}
