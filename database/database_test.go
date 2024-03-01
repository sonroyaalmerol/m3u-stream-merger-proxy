package database

import (
    "testing"
    "os"
	"path/filepath"
)

func TestInitializeSQLite(t *testing.T) {
	sqliteDBPath := filepath.Join(".", "data", "database.sqlite")

    // Test InitializeSQLite and check if the database file exists
    err := InitializeSQLite()
    if err != nil {
        t.Errorf("InitializeSQLite returned error: %v", err)
    }
    defer os.Remove(sqliteDBPath) // Cleanup the database file after the test
}

func TestSaveAndLoadFromSQLite(t *testing.T) {
    // Test LoadFromSQLite with existing data in the database
    expected := []StreamInfo{{
        Title: "stream1",
        TvgID: "test1",
        LogoURL: "http://test.com/image.png",
        Group: "test",
        URLs: []StreamURL{{
            Content: "testing",
            M3UIndex: 1,
        }},
    }, {
        Title: "stream2",
        TvgID: "test2",
        LogoURL: "http://test2.com/image.png",
        Group: "test2",
        URLs: []StreamURL{{
            Content: "testing2",
            M3UIndex: 2,
        }},
    }}
    err = SaveToSQLite(expected) // Insert test data into the database
    if err != nil {
        t.Errorf("SaveToSQLite returned error: %v", err)
    }

    result, err := LoadFromSQLite()
    if err != nil {
        t.Errorf("LoadFromSQLite returned error: %v", err)
    }

    if len(result) != len(expected) {
        t.Errorf("LoadFromSQLite returned %+v, expected %+v", result, expected)
    }
}
