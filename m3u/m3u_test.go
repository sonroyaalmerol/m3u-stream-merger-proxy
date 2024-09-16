package m3u

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"m3u-stream-merger/database"
)

func TestGenerateM3UContent(t *testing.T) {
	// Define a sample stream for testing
	stream := database.StreamInfo{
		Slug:    "test-stream",
		TvgID:   "1",
		Title:   "TestStream",
		LogoURL: "http://example.com/logo.png",
		Group:   "TestGroup",
		URLs:    map[int]string{0: "http://example.com/stream"},
	}

	os.Setenv("REDIS_DB", "2")

	db, err := database.InitializeDb()
	if err != nil {
		t.Errorf("InitializeDb returned error: %v", err)
	}

	err = db.ClearDb()
	if err != nil {
		t.Errorf("ClearDb returned error: %v", err)
	}

	err = db.SaveToDb([]*database.StreamInfo{&stream})
	if err != nil {
		t.Fatal(err)
	}

	// Create a new HTTP request
	req, err := http.NewRequest("GET", "/generate", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Create a ResponseRecorder to record the response
	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Handler(w, r)
	})

	// Call the ServeHTTP method of the handler to execute the test
	handler.ServeHTTP(rr, req)

	// Check the status code
	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v",
			status, http.StatusOK)
	}

	// Check the Content-Type header
	expectedContentType := "text/plain"
	if contentType := rr.Header().Get("Content-Type"); contentType != expectedContentType {
		t.Errorf("handler returned unexpected Content-Type: got %v want %v",
			contentType, expectedContentType)
	}

	// Check the generated M3U content
	expectedContent := fmt.Sprintf(`#EXTM3U
#EXTINF:-1 tvg-id="1" tvg-logo="http://example.com/logo.png" tvg-name="TestStream" group-title="TestGroup",TestStream
%s`, GenerateStreamURL("http://", stream))
	if rr.Body.String() != expectedContent {
		t.Errorf("handler returned unexpected body: got %v want %v",
			rr.Body.String(), expectedContent)
	}
}

func TestParseM3UFromURL(t *testing.T) {
	testM3UContent := `
#EXTM3U
#EXTINF:-1 channelID="x-ID.bcc1" tvg-chno="0.0" tvg-id="bbc1" tvg-name="BBC One" group-title="UK",BBC One
#EXTVLCOPT:http-user-agent=HbbTV/1.6.1
http://example.com/bbc1
#EXTINF:-1 channelID="x-ID.bcc2" tvg-chno="0.0" tvg-id="bbc2" tvg-name="BBC Two" group-title="UK",BBC Two
#EXTVLCOPT:http-user-agent=HbbTV/1.6.1
http://example.com/bbc2
#EXTINF:-1 channelID="x-ID.cnn" tvg-chno="0.0" tvg-id="cnn" tvg-name="CNN International" group-title="News",CNN International
#EXTVLCOPT:http-user-agent=HbbTV/1.6.1
http://example.com/cnn
#EXTVLCOPT:logo=http://example.com/bbc_logo.png
#EXTINF:-1 channelID="x-ID.fox" tvg-chno="0.0" tvg-name="FOX" group-title="Entertainment",FOX
http://example.com/fox
`
	// Create a mock HTTP server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		fmt.Fprintln(w, testM3UContent)
	}))
	defer mockServer.Close()

	// Create test file
	mockFile := "test-playlist.m3u"
	err := os.WriteFile(mockFile, []byte(testM3UContent), 0644)
	if err != nil {
		t.Errorf("WriteFile returned error: %v", err)
	}

	os.Setenv("REDIS_DB", "3")
	db, err := database.InitializeDb()
	if err != nil {
		t.Errorf("InitializeDb returned error: %v", err)
	}

	err = db.ClearDb()
	if err != nil {
		t.Errorf("ClearDb returned error: %v", err)
	}

	parser := InitializeParser()

	// Test the parseM3UFromURL function with the mock server URL
	err = parser.ParseURL(mockServer.URL, 0)
	if err != nil {
		t.Errorf("Error parsing M3U from URL: %v", err)
	}

	// Test the parseM3UFromURL function with the mock server URL
	err = parser.ParseURL(fmt.Sprintf("file://%s", mockFile), 1)
	if err != nil {
		t.Errorf("Error parsing M3U from URL: %v", err)
	}

	err = db.SaveToDb(parser.GetStreams())
	if err != nil {
		t.Errorf("Error store to db: %v", err)
	}

	// Verify expected values
	expectedStreams := []database.StreamInfo{
		{Slug: "bbc-one", Title: "BBC One", TvgChNo: "0.0", TvgID: "bbc1", Group: "UK", URLs: map[int]string{0: "http://example.com/bbc1", 1: "http://example.com/bbc1"}},
		{Slug: "bbc-two", Title: "BBC Two", TvgChNo: "0.0", TvgID: "bbc2", Group: "UK", URLs: map[int]string{0: "http://example.com/bbc2", 1: "http://example.com/bbc2"}},
		{Slug: "cnn-international", Title: "CNN International", TvgChNo: "0.0", TvgID: "cnn", Group: "News", URLs: map[int]string{0: "http://example.com/cnn", 1: "http://example.com/cnn"}},
		{Slug: "fox", Title: "FOX", TvgChNo: "0.0", Group: "Entertainment", URLs: map[int]string{0: "http://example.com/fox", 1: "http://example.com/fox"}},
	}

	streamChan := db.GetStreams()

	storedStreams := []database.StreamInfo{}
	for stream := range streamChan {
		storedStreams = append(storedStreams, stream)
	}

	// Compare the retrieved streams with the expected streams
	if len(storedStreams) != len(expectedStreams) {
		t.Fatalf("Expected %d streams, but got %d", len(expectedStreams), len(storedStreams))
	}

	// Create a map to store expected streams for easier comparison
	expectedMap := make(map[string]database.StreamInfo)
	for _, expected := range expectedStreams {
		expectedMap[expected.Title] = expected
	}

	for _, stored := range storedStreams {
		expected, ok := expectedMap[stored.Title]
		if !ok {
			t.Errorf("Unexpected stream with Title: %s", stored.Title)
			continue
		}
		if !streamInfoEqual(stored, expected) {
			t.Errorf("Stream with Title %s does not match expected content", stored.Title)
			t.Errorf("Stored: %#v, Expected: %#v", stored, expected)
		}
	}
}

// streamInfoEqual checks if two StreamInfo objects are equal.
func streamInfoEqual(a, b database.StreamInfo) bool {
	if a.Slug != b.Slug || a.TvgID != b.TvgID || a.TvgChNo != b.TvgChNo || a.Title != b.Title || a.Group != b.Group || a.LogoURL != b.LogoURL || len(a.URLs) != len(b.URLs) {
		return false
	}

	for i, url := range a.URLs {
		if url != b.URLs[i] {
			return false
		}
	}

	return true
}
