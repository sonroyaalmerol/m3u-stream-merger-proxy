package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"m3u-stream-merger/database"
	"m3u-stream-merger/m3u"
	"m3u-stream-merger/utils"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestMP4Handler(t *testing.T) {
	db, err := database.InitializeSQLite("current_streams")
	if err != nil {
		t.Errorf("InitializeSQLite returned error: %v", err)
	}

	err = database.InitializeMemDB()
	if err != nil {
		t.Errorf("Error initializing current memory database: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	os.Setenv("M3U_URL_1", "https://gist.githubusercontent.com/sonroyaalmerol/de1c90e8681af040924da5d15c7f530d/raw/06844df09e69ea278060252ca5aa8d767eb4543d/test-m3u.m3u")
	os.Setenv("BUFFER_MB", "3")

	updateSources(ctx)

	streams, err := db.GetStreams()
	if err != nil {
		t.Errorf("GetStreams returned error: %v", err)
	}

	m3uReq := httptest.NewRequest("GET", "/playlist.m3u", nil)
	m3uW := httptest.NewRecorder()

	func() {
		swappingLock.Lock()
		defer swappingLock.Unlock()

		m3u.GenerateM3UContent(m3uW, m3uReq, db)
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
			streamUid := utils.GetStreamUID(stream.Title)
			req := httptest.NewRequest("GET", fmt.Sprintf("/stream/%s.mp4", streamUid), nil)
			w := httptest.NewRecorder()

			// Call the handler function
			mp4Handler(w, req, db)

			// Check the response status code
			resp := w.Result()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("%s - Expected status code %d, got %d", stream.Title, http.StatusOK, resp.StatusCode)
			}

			res, err := http.Get(stream.URLs[0].Content)
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

	err = db.DeleteSQLite()
	if err != nil {
		t.Errorf("DeleteSQLite returned error: %v", err)
	}

	foldername := filepath.Join(".", "data")
	err = os.RemoveAll(foldername)
	if err != nil {
		t.Errorf("Error deleting data folder: %v\n", err)
	}
}
