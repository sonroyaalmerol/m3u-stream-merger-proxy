package database

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoadFromSQLite(t *testing.T) {
	// Test InitializeSQLite and check if the database file exists
	db, err := InitializeSQLite("test")
	if err != nil {
		t.Errorf("InitializeSQLite returned error: %v", err)
	}

	// Test LoadFromSQLite with existing data in the database
	expected := []StreamInfo{{
		DbId:    1,
		Title:   "stream1",
		TvgID:   "test1",
		LogoURL: "http://test.com/image.png",
		Group:   "test",
		URLs: []StreamURL{{
			DbId:     1,
			Content:  "testing",
			M3UIndex: 1,
		}},
	}, {
		DbId:    2,
		Title:   "stream2",
		TvgID:   "test2",
		LogoURL: "http://test2.com/image.png",
		Group:   "test2",
		URLs: []StreamURL{{
			DbId:     2,
			Content:  "testing2",
			M3UIndex: 2,
		}},
	}}

	err = db.SaveToSQLite(expected) // Insert test data into the database
	if err != nil {
		t.Errorf("SaveToSQLite returned error: %v", err)
	}

	result, err := db.GetStreams()
	if err != nil {
		t.Errorf("GetStreams returned error: %v", err)
	}

	if len(result) != len(expected) {
		t.Errorf("GetStreams returned %+v, expected %+v", result, expected)
	}

	for i, expectedStream := range expected {
		if !streamInfoEqual(result[i], expectedStream) {
			t.Errorf("GetStreams returned %+v, expected %+v", result[i], expectedStream)
		}
	}

	err = db.DeleteStreamByTitle(expected[1].Title)
	if err != nil {
		t.Errorf("DeleteStreamByTitle returned error: %v", err)
	}

	err = db.DeleteStreamURL(expected[0].URLs[0].DbId)
	if err != nil {
		t.Errorf("DeleteStreamURL returned error: %v", err)
	}

	result, err = db.GetStreams()
	if err != nil {
		t.Errorf("GetStreams returned error: %v", err)
	}

	expected = expected[:1]
	expected[0].URLs = make([]StreamURL, 0)

	if len(result) != len(expected) {
		t.Errorf("GetStreams returned %+v, expected %+v", result, expected)
	}

	for i, expectedStream := range expected {
		if !streamInfoEqual(result[i], expectedStream) {
			t.Errorf("GetStreams returned %+v, expected %+v", result[i], expectedStream)
		}
	}

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

// streamInfoEqual checks if two StreamInfo objects are equal.
func streamInfoEqual(a, b StreamInfo) bool {
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

func TestConcurrency(t *testing.T) {
	// Initialize the in-memory database
	err := InitializeMemDB()
	if err != nil {
		t.Errorf("Error initializing in-memory database: %v", err)
	}

	// Test IncrementConcurrency and GetConcurrency
	m3uIndex := 1
	err = IncrementConcurrency(m3uIndex)
	if err != nil {
		t.Errorf("Error incrementing concurrency: %v", err)
	}

	count, err := GetConcurrency(m3uIndex)
	if err != nil {
		t.Errorf("Error getting concurrency: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected concurrency count to be 1, got %d", count)
	}

	// Test DecrementConcurrency
	err = DecrementConcurrency(m3uIndex)
	if err != nil {
		t.Errorf("Error decrementing concurrency: %v", err)
	}

	count, err = GetConcurrency(m3uIndex)
	if err != nil {
		t.Errorf("Error getting concurrency: %v", err)
	}
	if count != 0 {
		t.Errorf("Expected concurrency count to be 0, got %d", count)
	}
}
