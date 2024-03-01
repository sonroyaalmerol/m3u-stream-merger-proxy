package m3u

import (
	"net/http"
	"net/http/httptest"
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

	// Set up the global Streams variable
	Streams = []database.StreamInfo{stream}

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
%s
`, generateStreamURL("http:///stream", "TestStream"))
	if rr.Body.String() != expectedContent {
		t.Errorf("handler returned unexpected body: got %v want %v",
			rr.Body.String(), expectedContent)
	}
}

func TestFindStreamByName(t *testing.T) {
	// Define a sample stream for testing
	stream := database.StreamInfo{
		TvgID:    "1",
		Title:    "TestStream",
		LogoURL:  "http://example.com/logo.png",
		Group:    "TestGroup",
		URLs:     []database.StreamURL{{Content: "http://example.com/stream"}},
	}

	// Set up the global Streams variable
	Streams = []database.StreamInfo{stream}

	// Test finding an existing stream by name
	foundStream, err := FindStreamByName("TestStream")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if foundStream.Title != stream.Title {
		t.Errorf("expected stream with title 'TestStream' but got: %s", foundStream.Title)
	}

	// Test finding a non-existing stream by name
	notFoundStream, err := FindStreamByName("NonExistentStream")
	if err == nil {
		t.Error("expected error for non-existent stream but got nil")
	}

	if notFoundStream.Title != "" {
		t.Errorf("expected empty stream title for non-existent stream but got: %s", notFoundStream.Title)
	}
}
