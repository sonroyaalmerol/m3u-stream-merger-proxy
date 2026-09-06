package sourceproc

import (
	"fmt"
	"m3u-stream-merger/config"
	"testing"

	"github.com/puzpuzpuz/xsync/v3"
)

func benchDataDir(b *testing.B) {
	b.Helper()
	dir := b.TempDir()
	prev := config.GetConfig()
	config.SetConfig(&config.Config{DataPath: dir, TempPath: dir})
	b.Cleanup(func() { config.SetConfig(prev) })
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
		URLs:        xsync.NewMapOf[string, map[string]string](),
	}
	s.URLs.Store("1", map[string]string{"abc": "1:::http://example.com/live/user/pass/1234.ts"})
	return s
}

// BenchmarkSanitizeField runs once per playlist entry during source processing.
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
		_ = formatStreamEntry("http://localhost:8080", s)
	}
}

// BenchmarkSortStreamSubUrls runs once per load-balancer attempt per index.
func BenchmarkSortStreamSubUrls(b *testing.B) {
	urls := make(map[string]string, 16)
	for i := range 16 {
		urls[fmt.Sprintf("hash%02d", i)] = fmt.Sprintf("%d:::http://example.com/live/%d.ts", i, i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = SortStreamSubUrls(urls)
	}
}
