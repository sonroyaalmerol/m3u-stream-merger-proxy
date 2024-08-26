package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"m3u-stream-merger/database"
	"m3u-stream-merger/m3u"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestStreamHandler(t *testing.T) {
	REDIS_ADDR := "127.0.0.1:6379"
	REDIS_PASS := ""
	REDIS_DB := 0

	db, err := database.InitializeDb(REDIS_ADDR, REDIS_PASS, REDIS_DB)
	if err != nil {
		t.Errorf("InitializeDb returned error: %v", err)
	}

	err = db.ClearDb()
	if err != nil {
		t.Errorf("ClearDb returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	os.Setenv("M3U_URL_1", "https://gist.githubusercontent.com/sonroyaalmerol/de1c90e8681af040924da5d15c7f530d/raw/06844df09e69ea278060252ca5aa8d767eb4543d/test-m3u.m3u")
	os.Setenv("BUFFER_MB", "3")
	os.Setenv("INCLUDE_GROUPS_1", "movies")

	updateSources(ctx, nil)

	streamChan := db.GetStreams()
	streams := []database.StreamInfo{}

	for stream := range streamChan {
		streams = append(streams, stream)
	}

	m3uReq := httptest.NewRequest("GET", "/playlist.m3u", nil)
	m3uW := httptest.NewRecorder()

	func() {
		m3u.Handler(m3uW, m3uReq, db)
	}()

	m3uResp := m3uW.Result()
	if m3uResp.StatusCode != http.StatusOK {
		t.Errorf("Playlist Route - Expected status code %d, got %d", http.StatusOK, m3uResp.StatusCode)
	}

	var wg sync.WaitGroup
	for _, stream := range streams {
		wg.Add(1)
		go func(stream database.StreamInfo) {
			defer wg.Done()
			log.Printf("Stream (%s): %v", stream.Title, stream)
			req := httptest.NewRequest("GET", strings.TrimSpace(m3u.GenerateStreamURL("", stream.Slug, stream.URLs[0])), nil)
			w := httptest.NewRecorder()

			// Call the handler function
			streamHandler(w, req, db)

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
		}(stream)
	}

	wg.Wait()
}
