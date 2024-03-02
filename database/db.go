package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
)

var db *sql.DB

func InitializeSQLite() error {
	foldername := filepath.Join(".", "data")
	filename := filepath.Join(foldername, "database.sqlite")

	err := os.MkdirAll(foldername, 0755)
	if err != nil {
		return fmt.Errorf("error creating data folder: %v\n", err)
	}

	file, err := os.OpenFile(filename, os.O_RDWR|os.O_CREATE, 0666)
	if err != nil {
		return fmt.Errorf("error creating database file: %v\n", err)
	}
	file.Close()

	db, err = sql.Open("sqlite3", filename)
	if err != nil {
		return fmt.Errorf("error opening SQLite database: %v\n", err)
	}

	// Create table if not exists
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS streams (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			title TEXT UNIQUE,
			tvg_id TEXT,
			logo_url TEXT,
			group_name TEXT
		)
	`)
	if err != nil {
		return fmt.Errorf("error creating table: %v\n", err)
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS stream_urls (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			stream_id INTEGER,
			content TEXT,
			m3u_index INTEGER,
			max_concurrency INTEGER DEFAULT 1,
			FOREIGN KEY(stream_id) REFERENCES streams(id)
		)
	`)
	if err != nil {
		return fmt.Errorf("error creating table: %v\n", err)
	}

	return nil
}

func SaveToSQLite(streams []StreamInfo) (err error) {
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

func InsertStream(s StreamInfo) (i int64, err error) {
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

func InsertStreamUrl(id int64, url StreamURL) (i int64, err error) {
	tx, err := db.Begin()
	if err != nil {
		return -1, fmt.Errorf("error beginning transaction: %v", err)
	}
	defer func() {
		if err != nil {
			err = tx.Rollback()
		}
	}()

	urlStmt, err := tx.Prepare("INSERT INTO stream_urls(stream_id, content, m3u_index, max_concurrency) VALUES(?, ?, ?, ?)")
	if err != nil {
		return -1, fmt.Errorf("error preparing statement: %v", err)
	}
	defer urlStmt.Close()

	res, err := urlStmt.Exec(id, url.Content, url.M3UIndex, url.MaxConcurrency)
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

func DeleteStreamByTitle(title string) error {
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

func DeleteStreamURL(streamURLID int64) error {
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

func GetStreamByTitle(title string) (s StreamInfo, err error) {
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

		urlRows, err := db.Query("SELECT id, content, m3u_index, max_concurrency FROM stream_urls WHERE stream_id = ?", s.DbId)
		if err != nil {
			return s, fmt.Errorf("error querying stream URLs: %v", err)
		}
		defer urlRows.Close()

		var urls []StreamURL
		for urlRows.Next() {
			var u StreamURL
			err := urlRows.Scan(&u.DbId, &u.Content, &u.M3UIndex, &u.MaxConcurrency)
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

func GetStreams() ([]StreamInfo, error) {
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

		urlRows, err := db.Query("SELECT id, content, m3u_index, max_concurrency FROM stream_urls WHERE stream_id = ?", s.DbId)
		if err != nil {
			return nil, fmt.Errorf("error querying stream URLs: %v", err)
		}
		defer urlRows.Close()

		var urls []StreamURL
		for urlRows.Next() {
			var u StreamURL
			err := urlRows.Scan(&u.DbId, &u.Content, &u.M3UIndex, &u.MaxConcurrency)
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
