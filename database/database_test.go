package database

import (
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
		Title:   "stream1",
		TvgID:   "test1",
		LogoURL: "http://test.com/image.png",
		Group:   "test",
		URLs: []StreamURL{{
			Content:  "testing",
			M3UIndex: 1,
		}},
	}, {
		Title:   "stream2",
		TvgID:   "test2",
		LogoURL: "http://test2.com/image.png",
		Group:   "test2",
		URLs: []StreamURL{{
			Content:  "testing2",
			M3UIndex: 2,
		}},
	}}

	err = SaveToSQLite(db, expected) // Insert test data into the database
	if err != nil {
		t.Errorf("SaveToSQLite returned error: %v", err)
	}

	result, err := GetStreams(db)
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

	err = DeleteStreamByTitle(db, expected[1].Title)
	if err != nil {
		t.Errorf("DeleteStreamByTitle returned error: %v", err)
	}

	result, err = GetStreams(db)
	if err != nil {
		t.Errorf("GetStreams returned error: %v", err)
	}

	expected = expected[:1]

	if len(result) != len(expected) {
		t.Errorf("GetStreams returned %+v, expected %+v", result, expected)
	}

	for i, expectedStream := range expected {
		if !streamInfoEqual(result[i], expectedStream) {
			t.Errorf("GetStreams returned %+v, expected %+v", result[i], expectedStream)
		}
	}

	err = DeleteSQLite("test")
	if err != nil {
		t.Errorf("DeleteSQLite returned error: %v", err)
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
