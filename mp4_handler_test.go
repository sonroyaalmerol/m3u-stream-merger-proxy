package main

import (
	"context"
	"fmt"
	"m3u-stream-merger/database"
	"m3u-stream-merger/utils"
	"net/http"
	"net/http/httptest"
	"os"
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

	os.Setenv("M3U_URL_1", "https://gist.githubusercontent.com/sonroyaalmerol/de1c90e8681af040924da5d15c7f530d/raw/1643b720210765bce1ef437f74630958c0911fb5/test-m3u.m3u")

	updateSources(ctx)

	streams, err := database.GetStreams(db)

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
		}(stream)
	}

	wg.Wait()

	err = database.DeleteSQLite("current_streams")
	if err != nil {
		t.Errorf("DeleteSQLite returned error: %v", err)
	}
}
