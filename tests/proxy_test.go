package tests

import (
	"bytes"
	"io"
	"log"
	"m3u-stream-merger/handlers"
	"m3u-stream-merger/store"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestStreamHandler(t *testing.T) {
	os.Setenv("M3U_URL_1", "https://gist.githubusercontent.com/sonroyaalmerol/de1c90e8681af040924da5d15c7f530d/raw/06844df09e69ea278060252ca5aa8d767eb4543d/test-m3u.m3u")
	os.Setenv("INCLUDE_GROUPS_1", "movies")

	err := store.DownloadM3USource(0)
	if err != nil {
		t.Errorf("Downloader returned error: %v", err)
	}

	streams := store.GetStreams()

	m3uReq := httptest.NewRequest("GET", "/playlist.m3u", nil)
	m3uW := httptest.NewRecorder()

	func() {
		handlers.M3UHandler(m3uW, m3uReq)
	}()

	m3uResp := m3uW.Result()
	if m3uResp.StatusCode != http.StatusOK {
		t.Errorf("Playlist Route - Expected status code %d, got %d", http.StatusOK, m3uResp.StatusCode)
	}

	cm := store.NewConcurrencyManager()

	for _, stream := range streams {
		log.Printf("Stream (%s): %v", stream.Title, stream)
		genStreamUrl := strings.TrimSpace(store.GenerateStreamURL("", stream))

		req := httptest.NewRequest("GET", genStreamUrl, nil)
		w := httptest.NewRecorder()

		// Call the handler function
		handlers.StreamHandler(w, req, cm)

		// Check the response status code
		resp := w.Result()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s - Expected status code %d, got %d", stream.Title, http.StatusOK, resp.StatusCode)
		}

		res, err := http.Get(stream.URLs[0])
		if err != nil {
			t.Errorf("HttpGet returned error: %v", err)
		}
		defer res.Body.Close()

		// Example of checking response body content
		expected, _ := io.ReadAll(res.Body)
		body, _ := io.ReadAll(resp.Body)
		if !bytes.Equal(body, expected) {
			t.Errorf("Streams did not match for: %s", stream.Title)
		}
	}
}
