package xtream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFetchListNormalizesProviderShapes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("action") {
		case "get_live_categories":
			_, _ = w.Write([]byte(`[{"category_id":"","category_name":"None","parent_id":""}]`))
		case "get_live_streams":
			_, _ = w.Write([]byte(`{"2":{"num":"2","name":"Second","stream_id":2,"stream_icon":null,"epg_channel_id":null,"category_id":""},"0":{"num":1,"name":"First","stream_id":"1","category_id":null}}`))
		case "get_vod_streams":
			_, _ = w.Write([]byte(`{}`))
		case "get_series":
			_, _ = w.Write([]byte(`null`))
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "user", "pass")
	categories, err := client.LiveCategories(context.Background())
	if err != nil || len(categories) != 1 || categories[0].CategoryID.String() != "" {
		t.Fatalf("categories = %#v, err = %v", categories, err)
	}
	live, err := client.LiveStreams(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 2 || live[0].Name != "First" || live[1].Name != "Second" {
		t.Fatalf("unexpected live order: %#v", live)
	}
	vod, err := client.VodStreams(context.Background())
	if err != nil || len(vod) != 0 {
		t.Fatalf("vod = %#v, err = %v", vod, err)
	}
	series, err := client.SeriesList(context.Background())
	if err != nil || len(series) != 0 {
		t.Fatalf("series = %#v, err = %v", series, err)
	}
}

func TestSeriesInfoNormalizesProviderShapes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("series_id") == "missing" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`{"info":{"last_modified":"1712345678","rating":7.5},"episodes":{"01":{"1":{"id":302,"episode_num":"2","container_extension":"mkv","movie_image":"top.jpg"},"0":{"id":"301","episode_num":"","container_extension":"mkv","info":{"movie_image":"nested.jpg"}}}}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "user", "pass")
	info, err := client.SeriesInfo(context.Background(), "7")
	if err != nil {
		t.Fatal(err)
	}
	episodes := info.Episodes["01"]
	if len(episodes) != 2 || episodes[0].ID.String() != "301" || episodes[1].EpisodeNum.Int() != 2 {
		t.Fatalf("unexpected episodes: %#v", episodes)
	}
	if episodes[0].Image() != "nested.jpg" || episodes[1].Image() != "top.jpg" {
		t.Fatalf("unexpected episode images: %q, %q", episodes[0].Image(), episodes[1].Image())
	}
	lines := SeriesToLines(client, "Show", "Drama", info)
	if !strings.Contains(strings.Join(lines, "\n"), `tvg-logo="nested.jpg"`) || !strings.Contains(strings.Join(lines, "\n"), "Show S1E1") {
		t.Fatalf("series lines did not use normalized metadata: %v", lines)
	}

	missing, err := client.SeriesInfo(context.Background(), "missing")
	if err != nil || len(missing.Episodes) != 0 {
		t.Fatalf("missing series = %#v, err = %v", missing, err)
	}
}

func TestFetchListRejectsInvalidRootWithoutRetry(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "error object", body: `{"error":"denied"}`, want: "non-numeric key"},
		{name: "multiple values", body: `[] []`, want: "multiple JSON values"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			_, err := NewClient(server.URL, "user", "pass").LiveStreams(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
			if calls.Load() != 1 {
				t.Fatalf("invalid shape retried %d times", calls.Load())
			}
		})
	}
}

func TestFetchPlaylistLinesRejectsCategoryFailure(t *testing.T) {
	panel := fakePanel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("action") == "get_live_categories" {
			http.Error(w, "unavailable", http.StatusBadRequest)
			return
		}
		panel.ServeHTTP(w, r)
	}))
	defer server.Close()

	err := FetchPlaylistLines(context.Background(), NewClient(server.URL, "user", "pass"), nil, func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "get_live_categories") {
		t.Fatalf("error = %v", err)
	}
}

func TestFetchPlaylistLinesSkipsInvalidEntries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("action") {
		case "get_live_categories", "get_vod_categories", "get_series_categories":
			_, _ = w.Write([]byte(`[]`))
		case "get_live_streams":
			_, _ = w.Write([]byte(`[{"name":"Valid","stream_id":"1"},{"name":"Missing ID","stream_id":""},{"name":"","stream_id":"3"}]`))
		case "get_vod_streams", "get_series":
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	defer server.Close()

	var lines []string
	err := FetchPlaylistLines(context.Background(), NewClient(server.URL, "user", "pass"), nil, func(line string) error {
		lines = append(lines, line)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || !strings.Contains(lines[0], "Valid") || !strings.HasSuffix(lines[1], "/1.ts") {
		t.Fatalf("unexpected lines: %v", lines)
	}
}
