package database

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

var mutex sync.Mutex

func checkAndUpdateTable(db *sql.DB, tableName string, expectedColumns map[string]string, foreignKeys map[string]string) error {
	// Check table existence
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", tableName).Scan(&count)
	if err != nil {
		return fmt.Errorf("error checking %s table: %v\n", tableName, err)
	}

	if count > 0 {
		// Table exists, check structure
		rows, err := db.Query("PRAGMA table_info(" + tableName + ")")
		if err != nil {
			return fmt.Errorf("error retrieving table info for %s: %v\n", tableName, err)
		}
		defer rows.Close()

		existingColumns := make(map[string]string)
		for rows.Next() {
			var cid int
			var name, _type string
			var notnull, pk int
			var dflt_value interface{}
			err = rows.Scan(&cid, &name, &_type, &notnull, &dflt_value, &pk)
			if err != nil {
				return fmt.Errorf("error scanning row: %v\n", err)
			}
			existingColumns[name] = _type
		}

		// Check if column names and types match expected structure
		for col, dataType := range expectedColumns {
			if existingType, ok := existingColumns[col]; !ok || existingType != dataType {
				// Table structure doesn't match, drop and recreate
				_, err = db.Exec("DROP TABLE " + tableName)
				if err != nil {
					return fmt.Errorf("error dropping %s table: %v\n", tableName, err)
				}
				break
			}
		}
	}

	// Create table if not exists or if dropped due to structure mismatch
	query := "CREATE TABLE IF NOT EXISTS " + tableName + " ("
	for col, dataType := range expectedColumns {
		query += col + " " + dataType + ","
	}
	if len(foreignKeys) > 0 {
		for fk := range foreignKeys {
			query += " " + fk + ","
		}
	}
	query = strings.TrimSuffix(query, ",") + ")"
	_, err = db.Exec(query)
	if err != nil {
		return fmt.Errorf("error creating %s table: %v\n", tableName, err)
	}

	return nil
}

func InitializeSQLite(name string) (db *sql.DB, err error) {
	mutex.Lock()
	defer mutex.Unlock()

	if db != nil {
		err := db.Close()
		if err == nil {
			log.Printf("Database session has already been closed: %v\n", err)
		}
	}

	foldername := filepath.Join(".", "data")
	filename := filepath.Join(foldername, fmt.Sprintf("%s.db", name))

	err = os.MkdirAll(foldername, 0755)
	if err != nil {
		return nil, fmt.Errorf("error creating data folder: %v\n", err)
	}

	file, err := os.OpenFile(filename, os.O_RDWR|os.O_CREATE, 0666)
	if err != nil {
		return nil, fmt.Errorf("error creating database file: %v\n", err)
	}
	file.Close()

	db, err = sql.Open("sqlite", filename)
	if err != nil {
		return nil, fmt.Errorf("error opening SQLite database: %v\n", err)
	}

	// Check and update 'streams' table
	if err := checkAndUpdateTable(db, "streams", map[string]string{
		"id":         "INTEGER PRIMARY KEY AUTOINCREMENT",
		"title":      "TEXT UNIQUE",
		"tvg_id":     "TEXT",
		"logo_url":   "TEXT",
		"group_name": "TEXT",
	}, nil); err != nil {
		return nil, err
	}

	// Check and update 'stream_urls' table
	if err := checkAndUpdateTable(db, "stream_urls", map[string]string{
		"id":        "INTEGER PRIMARY KEY AUTOINCREMENT",
		"stream_id": "INTEGER",
		"content":   "TEXT",
		"m3u_index": "INTEGER",
	}, map[string]string{
		"FOREIGN KEY(stream_id) REFERENCES streams(id)": "",
	}); err != nil {
		return nil, err
	}

	return
}

// DeleteSQLite deletes the SQLite database file.
func DeleteSQLite(name string) error {
	mutex.Lock()
	defer mutex.Unlock()

	foldername := filepath.Join(".", "data")
	filename := filepath.Join(foldername, fmt.Sprintf("%s.db", name))

	err := os.Remove(filename)
	if err != nil {
		return fmt.Errorf("error deleting database file: %v\n", err)
	}

	return nil
}

func RenameSQLite(prevName string, nextName string) error {
	mutex.Lock()
	defer mutex.Unlock()

	foldername := filepath.Join(".", "data")
	prevFileName := filepath.Join(foldername, fmt.Sprintf("%s.db", prevName))
	nextFileName := filepath.Join(foldername, fmt.Sprintf("%s.db", nextName))

	err := os.Rename(prevFileName, nextFileName)

	return err
}

func SaveToSQLite(db *sql.DB, streams []StreamInfo) (err error) {
	mutex.Lock()
	defer mutex.Unlock()

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("error beginning transaction: %v", err)
	}
	defer func() {
		if err != nil {
			err = tx.Rollback()
		}
	}()

	stmt, err := tx.Prepare("INSERT INTO streams(title, tvg_id, logo_url, group_name) VALUES(?, ?, ?, ?)")
	if err != nil {
		return fmt.Errorf("error preparing statement: %v", err)
	}
	defer stmt.Close()

	for _, s := range streams {
		res, err := stmt.Exec(s.Title, s.TvgID, s.LogoURL, s.Group)
		if err != nil {
			return fmt.Errorf("error inserting stream: %v", err)
		}

		streamID, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("error getting last inserted ID: %v", err)
		}

		urlStmt, err := tx.Prepare("INSERT INTO stream_urls(stream_id, content, m3u_index) VALUES(?, ?, ?)")
		if err != nil {
			return fmt.Errorf("error preparing statement: %v", err)
		}
		defer urlStmt.Close()

		for _, u := range s.URLs {
			_, err := urlStmt.Exec(streamID, u.Content, u.M3UIndex)
			if err != nil {
				return fmt.Errorf("error inserting stream URL: %v", err)
			}
		}
	}

	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("error committing transaction: %v", err)
	}

	return
}

func InsertStream(db *sql.DB, s StreamInfo) (i int64, err error) {
	mutex.Lock()
	defer mutex.Unlock()

	tx, err := db.Begin()
	if err != nil {
		return -1, fmt.Errorf("error beginning transaction: %v", err)
	}
	defer func() {
		if err != nil {
			err = tx.Rollback()
		}
	}()

	stmt, err := tx.Prepare("INSERT INTO streams(title, tvg_id, logo_url, group_name) VALUES(?, ?, ?, ?)")
	if err != nil {
		return -1, fmt.Errorf("error preparing statement: %v", err)
	}
	defer stmt.Close()

	res, err := stmt.Exec(s.Title, s.TvgID, s.LogoURL, s.Group)
	if err != nil {
		return -1, fmt.Errorf("error inserting stream: %v", err)
	}

	streamID, err := res.LastInsertId()
	if err != nil {
		return -1, fmt.Errorf("error getting last inserted ID: %v", err)
	}

	err = tx.Commit()
	if err != nil {
		return -1, fmt.Errorf("error committing transaction: %v", err)
	}
	return streamID, err
}

func InsertStreamUrl(db *sql.DB, id int64, url StreamURL) (i int64, err error) {
	mutex.Lock()
	defer mutex.Unlock()

	tx, err := db.Begin()
	if err != nil {
		return -1, fmt.Errorf("error beginning transaction: %v", err)
	}
	defer func() {
		if err != nil {
			err = tx.Rollback()
		}
	}()

	urlStmt, err := tx.Prepare("INSERT INTO stream_urls(stream_id, content, m3u_index) VALUES(?, ?, ?)")
	if err != nil {
		return -1, fmt.Errorf("error preparing statement: %v", err)
	}
	defer urlStmt.Close()

	res, err := urlStmt.Exec(id, url.Content, url.M3UIndex)
	if err != nil {
		return -1, fmt.Errorf("error inserting stream URL: %v", err)
	}

	insertedId, err := res.LastInsertId()
	if err != nil {
		return -1, fmt.Errorf("error getting last inserted ID: %v", err)
	}

	err = tx.Commit()
	if err != nil {
		return -1, fmt.Errorf("error committing transaction: %v", err)
	}

	return insertedId, err
}

func DeleteStreamByTitle(db *sql.DB, title string) error {
	mutex.Lock()
	defer mutex.Unlock()

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("error beginning transaction: %v", err)
	}
	defer func() {
		if err != nil {
			err = tx.Rollback()
		}
	}()

	stmt, err := tx.Prepare("DELETE FROM streams WHERE title = ?")
	if err != nil {
		return fmt.Errorf("error preparing statement: %v", err)
	}
	defer stmt.Close()

	_, err = stmt.Exec(title)
	if err != nil {
		return fmt.Errorf("error deleting stream: %v", err)
	}

	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("error committing transaction: %v", err)
	}

	return nil
}

func DeleteStreamURL(db *sql.DB, streamURLID int64) error {
	mutex.Lock()
	defer mutex.Unlock()

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("error beginning transaction: %v", err)
	}
	defer func() {
		if err != nil {
			err = tx.Rollback()
		}
	}()

	stmt, err := tx.Prepare("DELETE FROM stream_urls WHERE id = ?")
	if err != nil {
		return fmt.Errorf("error preparing statement: %v", err)
	}
	defer stmt.Close()

	_, err = stmt.Exec(streamURLID)
	if err != nil {
		return fmt.Errorf("error deleting stream URL: %v", err)
	}

	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("error committing transaction: %v", err)
	}

	return nil
}

func GetStreamByTitle(db *sql.DB, title string) (s StreamInfo, err error) {
	mutex.Lock()
	defer mutex.Unlock()

	rows, err := db.Query("SELECT id, title, tvg_id, logo_url, group_name FROM streams WHERE title = ?", title)
	if err != nil {
		return s, fmt.Errorf("error querying streams: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		err = rows.Scan(&s.DbId, &s.Title, &s.TvgID, &s.LogoURL, &s.Group)
		if err != nil {
			return s, fmt.Errorf("error scanning stream: %v", err)
		}

		urlRows, err := db.Query("SELECT id, content, m3u_index FROM stream_urls WHERE stream_id = ?", s.DbId)
		if err != nil {
			return s, fmt.Errorf("error querying stream URLs: %v", err)
		}
		defer urlRows.Close()

		var urls []StreamURL
		for urlRows.Next() {
			var u StreamURL
			err := urlRows.Scan(&u.DbId, &u.Content, &u.M3UIndex)
			if err != nil {
				return s, fmt.Errorf("error scanning stream URL: %v", err)
			}
			urls = append(urls, u)
		}
		if err := urlRows.Err(); err != nil {
			return s, fmt.Errorf("error iterating over URL rows: %v", err)
		}

		s.URLs = urls
		if err := rows.Err(); err != nil {
			return s, fmt.Errorf("error iterating over rows: %v", err)
		}
	}

	if err := rows.Err(); err != nil {
		return s, fmt.Errorf("error iterating over rows: %v", err)
	}

	return s, nil
}

func GetStreamUrlByUrlAndIndex(db *sql.DB, url string, m3u_index int) (s StreamURL, err error) {
	mutex.Lock()
	defer mutex.Unlock()

	rows, err := db.Query("SELECT id, content, m3u_index FROM stream_urls WHERE content = ? AND m3u_index = ?", url, m3u_index)
	if err != nil {
		return s, fmt.Errorf("error querying streams: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		err = rows.Scan(&s.DbId, &s.Content, &s.M3UIndex)
		if err != nil {
			return s, fmt.Errorf("error scanning stream: %v", err)
		}
	}

	if err := rows.Err(); err != nil {
		return s, fmt.Errorf("error iterating over rows: %v", err)
	}

	return s, nil
}

func GetStreams(db *sql.DB) ([]StreamInfo, error) {
	mutex.Lock()
	defer mutex.Unlock()

	rows, err := db.Query("SELECT id, title, tvg_id, logo_url, group_name FROM streams")
	if err != nil {
		return nil, fmt.Errorf("error querying streams: %v", err)
	}
	defer rows.Close()

	var streams []StreamInfo
	for rows.Next() {
		var s StreamInfo
		err := rows.Scan(&s.DbId, &s.Title, &s.TvgID, &s.LogoURL, &s.Group)
		if err != nil {
			return nil, fmt.Errorf("error scanning stream: %v", err)
		}

		urlRows, err := db.Query("SELECT id, content, m3u_index FROM stream_urls WHERE stream_id = ?", s.DbId)
		if err != nil {
			return nil, fmt.Errorf("error querying stream URLs: %v", err)
		}
		defer urlRows.Close()

		var urls []StreamURL
		for urlRows.Next() {
			var u StreamURL
			err := urlRows.Scan(&u.DbId, &u.Content, &u.M3UIndex)
			if err != nil {
				return nil, fmt.Errorf("error scanning stream URL: %v", err)
			}
			urls = append(urls, u)
		}
		if err := urlRows.Err(); err != nil {
			return nil, fmt.Errorf("error iterating over URL rows: %v", err)
		}

		s.URLs = urls
		streams = append(streams, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating over rows: %v", err)
	}

	return streams, nil
}
