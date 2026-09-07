package utils

import (
	"net/http"
	"testing"
)

func TestIsATSMedia(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "http://up/live/u/p/1611001.ts", nil)
	resp := &http.Response{
		Header:  http.Header{"Content-Type": []string{"video/mp2t"}},
		Request: req,
	}
	if !IsATSMedia(resp) {
		t.Fatal("expected .ts with mp2t content type to be MPEG-TS")
	}

	resp.Header = http.Header{"Content-Type": []string{"application/octet-stream"}}
	if !IsATSMedia(resp) {
		t.Fatal("expected .ts URL to be MPEG-TS regardless of content type")
	}

	req2, _ := http.NewRequest(http.MethodGet, "http://up/movie/u/p/42.mp4", nil)
	resp2 := &http.Response{Header: http.Header{"Content-Type": []string{"video/mp4"}}, Request: req2}
	if IsATSMedia(resp2) {
		t.Fatal("expected .mp4 to not be MPEG-TS")
	}

	resp3 := &http.Response{Header: http.Header{"Content-Type": []string{"application/vnd.apple.mpegurl"}}}
	if IsATSMedia(resp3) {
		t.Fatal("expected m3u8 to not be MPEG-TS")
	}
}
