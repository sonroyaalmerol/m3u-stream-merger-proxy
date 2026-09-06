package xtream

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakePanel() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/player_api.php", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("username") != "user" || query.Get("password") != "pass" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch query.Get("action") {
		case "get_live_categories":
			fmt.Fprint(w, `[{"category_id":"1","category_name":"News","parent_id":0}]`)
		case "get_vod_categories":
			fmt.Fprint(w, `[{"category_id":"2","category_name":"Movies","parent_id":0}]`)
		case "get_series_categories":
			fmt.Fprint(w, `[{"category_id":"3","category_name":"Drama","parent_id":0}]`)
		case "get_live_streams":
			fmt.Fprint(w, `[{"num":1,"name":"CNN","stream_type":"live","stream_id":100,"stream_icon":"http://img/cnn.png","epg_channel_id":"cnn.id","category_id":"1"}]`)
		case "get_vod_streams":
			fmt.Fprint(w, `[{"num":1,"name":"Cool Movie","stream_type":"movie","stream_id":200,"stream_icon":"http://img/m.png","category_id":"2","container_extension":"mp4"}]`)
		case "get_series":
			fmt.Fprint(w, `[{"num":1,"name":"Test Show","series_id":300,"cover":"http://img/show.png","category_id":"3"}]`)
		case "get_series_info":
			fmt.Fprint(w, `{"info":{"name":"Test Show"},"episodes":{"1":[{"id":301,"episode_num":2,"title":"Episode 2","container_extension":"mkv","movie_image":"http://img/ep.png"}]}}`)
		default:
			fmt.Fprint(w, `[]`)
		}
	})
	return mux
}

func TestFetchPlaylistLines(t *testing.T) {
	server := httptest.NewServer(fakePanel())
	defer server.Close()

	client := NewClient(server.URL, "user", "pass")

	var lines []string
	err := FetchPlaylistLines(context.Background(), client, func(line string) error {
		lines = append(lines, line)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(lines, "\n")
	wantSubstrings := []string{
		`#EXTINF:-1 tvg-name="CNN" tvg-id="cnn.id" tvg-type="live" tvg-logo="http://img/cnn.png" tvg-group="News" group-title="News",CNN`,
		fmt.Sprintf("%s/live/user/pass/100.ts", server.URL),
		`#EXTINF:-1 tvg-name="Cool Movie" tvg-type="movie" tvg-logo="http://img/m.png" tvg-group="Movies" group-title="Movies",Cool Movie`,
		fmt.Sprintf("%s/movie/user/pass/200.mp4", server.URL),
		`#EXTINF:-1 tvg-name="Test Show S1E2" tvg-type="series" tvg-logo="http://img/ep.png" tvg-group="Drama" group-title="Drama",Test Show S1E2`,
		fmt.Sprintf("%s/series/user/pass/301.mkv", server.URL),
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(joined, want) {
			t.Errorf("missing line:\n%s\ngot:\n%s", want, joined)
		}
	}
}

func TestFetchPlaylistLinesBadAuth(t *testing.T) {
	server := httptest.NewServer(fakePanel())
	defer server.Close()

	client := NewClient(server.URL, "user", "wrong")
	err := FetchPlaylistLines(context.Background(), client, func(string) error { return nil })
	if err == nil {
		t.Fatal("expected error for bad credentials")
	}
}
