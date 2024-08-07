package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "github.com/mattn/go-sqlite3"
)

type Instance struct {
	Sql       *sql.DB
	FileName  string
	FileLock  sync.Mutex
	WriteLock sync.Mutex
}

func InitializeSQLite(name string) (db *Instance, err error) {
	db = new(Instance)

	db.FileLock.Lock()
	db.WriteLock.Lock()
	defer db.FileLock.Unlock()
	defer db.WriteLock.Unlock()

	foldername := filepath.Join(".", "data")
	db.FileName = filepath.Join(foldername, fmt.Sprintf("%s.db", name))

	err = os.MkdirAll(foldername, 0755)
	if err != nil {
		return nil, fmt.Errorf("error creating data folder: %v\n", err)
	}

	file, err := os.OpenFile(db.FileName, os.O_RDWR|os.O_CREATE, 0666)
	if err != nil {
		return nil, fmt.Errorf("error creating database file: %v\n", err)
	}
	file.Close()

	db.Sql, err = sql.Open("sqlite3", db.FileName)
	if err != nil {
		return nil, fmt.Errorf("error opening SQLite database: %v\n", err)
	}

	_, err = db.Sql.Exec("PRAGMA journal_mode=WAL;")
	if err != nil {
		return nil, fmt.Errorf("error enabling wal mode: %v\n", err)
	}

	_, err = db.Sql.Exec("PRAGMA synchronous=normal;")
	if err != nil {
		return nil, fmt.Errorf("error enabling wal mode: %v\n", err)
	}

	_, err = db.Sql.Exec("PRAGMA journal_size_limit=6144000;")
	if err != nil {
		return nil, fmt.Errorf("error enabling wal mode: %v\n", err)
	}

	// Create table if not exists
	_, err = db.Sql.Exec(`
		CREATE TABLE IF NOT EXISTS streams (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			title TEXT UNIQUE,
			tvg_id TEXT,
			logo_url TEXT,
			group_name TEXT
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("error creating table: %v\n", err)
	}

	_, err = db.Sql.Exec(`
		CREATE TABLE IF NOT EXISTS stream_urls (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			stream_id INTEGER,
			content TEXT,
			m3u_index INTEGER,
			FOREIGN KEY(stream_id) REFERENCES streams(id)
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("error creating table: %v\n", err)
	}

	return
}

// DeleteSQLite deletes the SQLite database file.
func (db *Instance) DeleteSQLite() error {
	db.FileLock.Lock()
	db.WriteLock.Lock()
	defer db.FileLock.Unlock()
	defer db.WriteLock.Unlock()

	_ = db.Sql.Close()

	err := os.Remove(db.FileName)
	if err != nil {
		return fmt.Errorf("error deleting database file: %v\n", err)
	}

	db.Sql = nil

	return nil
}

func (db *Instance) RenameSQLite(newName string) error {
	db.FileLock.Lock()
	db.WriteLock.Lock()
	defer db.FileLock.Unlock()
	defer db.WriteLock.Unlock()

	_ = db.Sql.Close()

	foldername := filepath.Join(".", "data")
	nextFileName := filepath.Join(foldername, fmt.Sprintf("%s.db", newName))

	err := os.Rename(db.FileName, nextFileName)
	if err != nil {
		return fmt.Errorf("error renaming database file: %v\n", err)
	}

	db.FileName = nextFileName
	db.Sql, err = sql.Open("sqlite3", db.FileName)
	if err != nil {
		return fmt.Errorf("error opening SQLite database: %v\n", err)
	}

	return err
}

func (db *Instance) SaveToSQLite(streams []StreamInfo) (err error) {
	db.WriteLock.Lock()
	defer db.WriteLock.Unlock()

	tx, err := db.Sql.Begin()
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

func (db *Instance) InsertStream(s StreamInfo) (i int64, err error) {
	db.WriteLock.Lock()
	defer db.WriteLock.Unlock()

	tx, err := db.Sql.Begin()
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

func (db *Instance) InsertStreamUrl(id int64, url StreamURL) (i int64, err error) {
	db.WriteLock.Lock()
	defer db.WriteLock.Unlock()

	tx, err := db.Sql.Begin()
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

func (db *Instance) DeleteStreamByTitle(title string) error {
	db.WriteLock.Lock()
	defer db.WriteLock.Unlock()

	tx, err := db.Sql.Begin()
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

func (db *Instance) DeleteStreamURL(streamURLID int64) error {
	db.WriteLock.Lock()
	defer db.WriteLock.Unlock()

	tx, err := db.Sql.Begin()
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

func (db *Instance) GetStreamByTitle(title string) (s StreamInfo, err error) {
	rows, err := db.Sql.Query("SELECT id, title, tvg_id, logo_url, group_name FROM streams WHERE title = ?", title)
	if err != nil {
		return s, fmt.Errorf("error querying streams: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		err = rows.Scan(&s.DbId, &s.Title, &s.TvgID, &s.LogoURL, &s.Group)
		if err != nil {
			return s, fmt.Errorf("error scanning stream: %v", err)
		}

		urlRows, err := db.Sql.Query("SELECT id, content, m3u_index FROM stream_urls WHERE stream_id = ? ORDER BY m3u_index ASC", s.DbId)
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

func (db *Instance) GetStreamUrlByUrlAndIndex(url string, m3u_index int) (s StreamURL, err error) {
	rows, err := db.Sql.Query("SELECT id, content, m3u_index FROM stream_urls WHERE content = ? AND m3u_index = ? ORDER BY m3u_index ASC", url, m3u_index)
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

func (db *Instance) GetStreams() ([]StreamInfo, error) {
	rows, err := db.Sql.Query("SELECT id, title, tvg_id, logo_url, group_name FROM streams ORDER BY CAST(tvg_id AS INTEGER) ASC")
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

		urlRows, err := db.Sql.Query("SELECT id, content, m3u_index FROM stream_urls WHERE stream_id = ? ORDER BY m3u_index ASC", s.DbId)
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
