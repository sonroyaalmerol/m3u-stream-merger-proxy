package m3u

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"m3u-stream-merger/database"
)

func TestGenerateM3UContent(t *testing.T) {
	// Define a sample stream for testing
	stream := database.StreamInfo{
		TvgID:    "1",
		Title:    "TestStream",
		LogoURL:  "http://example.com/logo.png",
		Group:    "TestGroup",
		URLs:     []database.StreamURL{{Content: "http://example.com/stream"}},
	}

  sqliteDBPath := filepath.Join(".", "data", "database.sqlite")

  // Test InitializeSQLite and check if the database file exists
  err := database.InitializeSQLite()
  if err != nil {
      t.Errorf("InitializeSQLite returned error: %v", err)
  }
  defer os.Remove(sqliteDBPath) // Cleanup the database file after the test

  err = database.InsertStream(stream)
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
	handler := http.HandlerFunc(GenerateM3UContent)

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
#EXTINF:-1 tvg-id="1" tvg-name="TestStream" tvg-logo="http://example.com/logo.png" group-title="TestGroup",TestStream
%s`, generateStreamURL("http:///stream", "TestStream"))
	if rr.Body.String() != expectedContent {
		t.Errorf("handler returned unexpected body: got %v want %v",
			rr.Body.String(), expectedContent)
	}
}

// TestParseM3UFile tests the parseM3UFile function.
func TestParseM3UFile(t *testing.T) {
  sqliteDBPath := filepath.Join(".", "data", "database.sqlite")

  // Test InitializeSQLite and check if the database file exists
  err := database.InitializeSQLite()
  if err != nil {
      t.Errorf("InitializeSQLite returned error: %v", err)
  }
  defer os.Remove(sqliteDBPath) // Cleanup the database file after the test

	// Create a temporary test file
	tempFile, err := os.CreateTemp("", "test.m3u")
	if err != nil {
		t.Fatalf("Failed to create temporary file: %v", err)
	}
	defer os.Remove(tempFile.Name()) // Clean up

	// Write test data to the temporary file
	testData := []byte(`
#EXTINF:-1 tvg-id="channel1" tvg-name="Channel 1" group-title="Group 1" tvg-logo="logo1.jpg",Channel 1
http://example.com/channel1
#EXTINF:-1 tvg-id="channel2" tvg-name="Channel 2" group-title="Group 2" tvg-logo="logo2.jpg",Channel 2
http://example.com/channel2
#EXTVLCOPT:logo=http://example.com/logo3.jpg
http://example.com/channel3
	`)
	if _, err := tempFile.Write(testData); err != nil {
		t.Fatalf("Failed to write to temporary file: %v", err)
	}
	tempFile.Close() // Close the file before parsing

	// Call the function with the temporary file
	err = parseM3UFile(tempFile.Name(), 0)
	if err != nil {
		t.Fatalf("Error parsing M3U file: %v", err)
	}

  // Check the expected content in the database
	expectedStreams := []database.StreamInfo{
		{URLs: []database.StreamURL{{Content: "http://example.com/channel1", M3UIndex: 0}}, TvgID: "channel1", Title: "Channel 1", Group: "Group 1", LogoURL: "logo1.jpg"},
		{URLs: []database.StreamURL{{Content: "http://example.com/channel2", M3UIndex: 0}}, TvgID: "channel2", Title: "Channel 2", Group: "Group 2", LogoURL: "logo2.jpg"},
	}

	// Retrieve streams from the database
	storedStreams, err := database.GetStreams()
	if err != nil {
		t.Fatalf("Error retrieving streams from database: %v", err)
	}

	// Compare the retrieved streams with the expected streams
	if len(storedStreams) != len(expectedStreams) {
		t.Fatalf("Expected %d streams, but got %d", len(expectedStreams), len(storedStreams))
	}

	for i, expected := range expectedStreams {
		if !streamInfoEqual(storedStreams[i], expected) {
			t.Fatalf("Stream at index %d does not match expected content", i)
		}
	}
}


// streamInfoEqual checks if two StreamInfo objects are equal.
func streamInfoEqual(a, b database.StreamInfo) bool {
	if a.TvgID != b.TvgID || a.Title != b.Title || a.Group != b.Group || a.LogoURL != b.LogoURL || len(a.URLs) != len(b.URLs) {
		return false
	}

	for i, url := range a.URLs {
		if url.Content != b.URLs[i].Content || url.M3UIndex != b.URLs[i].M3UIndex {
			return false
		}
	}

	return true
}
