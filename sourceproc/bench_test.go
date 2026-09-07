package sourceproc

import (
	"fmt"
	"sync"
	"testing"

	"m3u-stream-merger/config"
)

func benchDataDir(b testing.TB) {
	b.Helper()
	dir := b.TempDir()
	prev := config.GetConfig()
	config.SetConfig(&config.Config{DataPath: dir, TempPath: dir})
	b.Cleanup(func() { config.SetConfig(prev) })

	defaultStore.mu.Lock()
	defaultStore.loaded = false
	if defaultStore.data != nil {
		_ = defaultStore.data.Close()
		defaultStore.data = nil
	}
	defaultStore.index = nil
	defaultStore.mu.Unlock()
}

func benchStream(i int) *StreamInfo {
	s := &StreamInfo{
		Title:       fmt.Sprintf("Some Channel Name HD %d", i),
		TvgID:       fmt.Sprintf("chan.%d.tv", i),
		TvgChNo:     fmt.Sprintf("%d", i),
		TvgType:     "live",
		LogoURL:     "http://example.com/logo/channel.png",
		Group:       "Sports | International",
		SourceM3U:   "1",
		SourceIndex: i,
	}
	s.AddURL("1", 1, "http://example.com/live/user/pass/1234.ts")
	return s
}

func benchStreams(n int) []*StreamInfo {
	streams := make([]*StreamInfo, n)
	unique := max(n*4/5, 1)
	for i := range streams {
		streams[i] = benchStream(i % unique)
	}
	return streams
}

func BenchmarkSortingPipeline(b *testing.B) {
	for _, n := range []int{1000, 20000, 100000} {
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
				err := m.MergeRendered(func(*StreamInfo, *renderBuf) renderedEntry { return renderedEntry{} },
					func(renderedEntry) error { count++; return nil })
				if err != nil {
					b.Fatal(err)
				}

				b.StopTimer()
				if count == 0 {
					b.Fatal("no entries emitted")
				}
				m.Close()
				b.StartTimer()
			}
		})
	}
}

func BenchmarkSortingParallelInsert(b *testing.B) {
	const n = 10000
	const workers = 8
	benchDataDir(b)
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		streams := benchStreams(n)
		m := newSpillSorter()
		b.StartTimer()

		var wg sync.WaitGroup
		for w := range workers {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				for i := w; i < len(streams); i += workers {
					if err := m.Add(streams[i]); err != nil {
						b.Error(err)
						return
					}
				}
			}(w)
		}
		wg.Wait()

		b.StopTimer()
		m.Close()
		b.StartTimer()
	}
}

func BenchmarkSanitizeField(b *testing.B) {
	title := "Some | Channel: Name/HD <Sports> \"Feed\" ?1"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = sanitizeField(title)
	}
}

// BenchmarkFormatStreamEntry runs once per entry when compiling the merged M3U.
func BenchmarkFormatStreamEntry(b *testing.B) {
	benchDataDir(b)
	s := benchStream(42)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = formatStreamEntry("http://localhost:8080", slugSum(s.Title), s)
	}
}
