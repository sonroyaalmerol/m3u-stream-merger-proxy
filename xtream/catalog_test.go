package xtream

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTestM3U(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "merged.m3u")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

const testM3U = `#EXTM3U
#EXTINF:-1 tvg-id="cnn.id" tvg-name="CNN" tvg-type="live" tvg-group="News" tvg-logo="http://img/cnn.png",CNN
http://base:8080/p/live/u/p/SLUG_LIVE.ts
#EXTINF:-1 tvg-name="BBC" tvg-group="News",BBC
http://base:8080/p/stream/SLUG_FALLBACK
#EXTINF:-1 tvg-type="movie" tvg-group="Movies" tvg-logo="http://img/m.png",Cool Movie
http://base:8080/p/movie/u/p/SLUG_MOVIE.mp4
#EXTINF:-1 tvg-type="series" tvg-group="Drama",Test Show S01E02
http://base:8080/p/series/u/p/SLUG_EP2.mkv
#EXTINF:-1 tvg-type="series" tvg-group="Drama",Test Show S01E01
http://base:8080/p/series/u/p/SLUG_EP1.mkv
#EXTINF:-1 tvg-type="series" tvg-group="Drama",Test Show S02E01
http://base:8080/p/series/u/p/SLUG_S2E1.mkv
`

func TestCatalogRebuild(t *testing.T) {
	c := NewCatalog()
	if err := c.Rebuild(writeTestM3U(t, testM3U)); err != nil {
		t.Fatal(err)
	}

	if got := len(c.Live(0)); got != 2 {
		t.Errorf("live entries = %d, want 2", got)
	}
	if got := len(c.Vod(0)); got != 1 {
		t.Errorf("vod entries = %d, want 1", got)
	}

	live := c.Live(0)
	first := live[0]
	if first.Title != "BBC" && first.Title != "CNN" {
		t.Errorf("unexpected live title %q", first.Title)
	}
	cnn := c.byID[streamID("CNN")]
	if cnn == nil {
		t.Fatal("CNN not indexed by stream id")
	}
	if cnn.TvgID != "cnn.id" || cnn.Group != "News" || cnn.Slug != "SLUG_LIVE" || cnn.BasePath != "live/u/p" || cnn.Ext != ".ts" {
		t.Errorf("CNN entry wrong: %+v", cnn)
	}

	bbc := c.byID[streamID("BBC")]
	if bbc == nil || bbc.Type != TypeLive {
		t.Errorf("fallback type inference failed: %+v", bbc)
	}
	if bbc.Ext != "" {
		t.Errorf("fallback ext = %q, want empty", bbc.Ext)
	}

	if got := c.Categories(TypeLive); len(got) != 1 || got[0].CategoryName != "News" {
		t.Errorf("live categories = %+v", got)
	}
	if got := c.Categories(TypeSeries); len(got) != 1 || got[0].CategoryName != "Drama" {
		t.Errorf("series categories = %+v", got)
	}

	sd := c.SeriesInfo(seriesID("Test Show"))
	if sd == nil {
		t.Fatal("series not grouped")
	}
	if len(sd.Episodes[1]) != 2 || sd.Episodes[1][0].Episode != 1 || sd.Episodes[1][1].Episode != 2 {
		t.Errorf("season 1 episodes wrong: %+v", sd.Episodes[1])
	}
	if len(sd.Episodes[2]) != 1 {
		t.Errorf("season 2 episodes = %d, want 1", len(sd.Episodes[2]))
	}

	ep := c.FindStream(streamID("Test Show S02E01"))
	if ep == nil || ep.Show != "Test Show" || ep.Season != 2 {
		t.Errorf("episode lookup failed: %+v", ep)
	}
}

func TestCatalogCategoryFilter(t *testing.T) {
	c := NewCatalog()
	if err := c.Rebuild(writeTestM3U(t, testM3U)); err != nil {
		t.Fatal(err)
	}

	newsID := categoryID(TypeLive + "|News")
	if got := len(c.Live(newsID)); got != 2 {
		t.Errorf("filtered live = %d, want 2", got)
	}
	if got := len(c.Live(999999)); got != 0 {
		t.Errorf("unknown category live = %d, want 0", got)
	}
	if got := len(c.Vod(categoryID(TypeMovie + "|Movies"))); got != 1 {
		t.Errorf("filtered vod = %d, want 1", got)
	}
	if got := len(c.SeriesList(categoryID(TypeSeries + "|Drama"))); got != 1 {
		t.Errorf("filtered series = %d, want 1", got)
	}
}
