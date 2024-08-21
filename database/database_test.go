package database

import (
	"testing"
)

func TestSaveAndLoadFromDb(t *testing.T) {
	// Test InitializeDb and check if the database file exists
	REDIS_ADDR := "127.0.0.1:6379"
	REDIS_PASS := ""
	REDIS_DB := 1

	db, err := InitializeDb(REDIS_ADDR, REDIS_PASS, REDIS_DB)
	if err != nil {
		t.Errorf("InitializeDb returned error: %v", err)
	}

	err = db.ClearDb()
	if err != nil {
		t.Errorf("ClearDb returned error: %v", err)
	}

	// Test LoadFromDb with existing data in the database
	expected := []StreamInfo{{
		Slug:    "stream1",
		Title:   "stream1",
		TvgID:   "test1",
		LogoURL: "http://test.com/image.png",
		Group:   "test",
		URLs:    map[int]string{0: "testing"},
	}, {
		Slug:    "stream2",
		Title:   "stream2",
		TvgID:   "test2",
		LogoURL: "http://test2.com/image.png",
		Group:   "test2",
		URLs:    map[int]string{0: "testing2"},
	}}

	err = db.SaveToDb(expected) // Insert test data into the database
	if err != nil {
		t.Errorf("SaveToDb returned error: %v", err)
	}

	streamChan := make(chan []StreamInfo)
	errChan := make(chan error)
	defer close(streamChan)
	defer close(errChan)
	result, err := db.GetStreams(streamChan, errChan)
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

	err = db.DeleteStreamBySlug(expected[1].Slug)
	if err != nil {
		t.Errorf("DeleteStreamBySlug returned error: %v", err)
	}

	err = db.DeleteStreamURL(expected[0], 0)
	if err != nil {
		t.Errorf("DeleteStreamURL returned error: %v", err)
	}

	streamChan = make(chan []StreamInfo)
	errChan = make(chan error)
	result, err = db.GetStreams(streamChan, errChan)
	if err != nil {
		t.Errorf("GetStreams returned error: %v", err)
	}

	expected = expected[:1]
	expected[0].URLs = map[int]string{}

	if len(result) != len(expected) {
		t.Errorf("GetStreams returned %+v, expected %+v", result, expected)
	}

	for i, expectedStream := range expected {
		if !streamInfoEqual(result[i], expectedStream) {
			t.Errorf("GetStreams returned %+v, expected %+v", result[i], expectedStream)
		}
	}

}

// streamInfoEqual checks if two StreamInfo objects are equal.
func streamInfoEqual(a, b StreamInfo) bool {
	if a.Slug != b.Slug || a.TvgID != b.TvgID || a.Title != b.Title || a.Group != b.Group || a.LogoURL != b.LogoURL || len(a.URLs) != len(b.URLs) {
		return false
	}

	for i, url := range a.URLs {
		if url != b.URLs[i] {
			return false
		}
	}

	return true
}
