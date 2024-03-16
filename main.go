package main

import (
	"context"
	"fmt"
	"log"
	"m3u-stream-merger/database"
	"m3u-stream-merger/m3u"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

var db *database.Instance
var cronMutex sync.Mutex
var swappingLock sync.Mutex

func swapDb(newInstance *database.Instance) error {
	swappingLock.Lock()
	defer swappingLock.Unlock()

	if db == nil {
		err := newInstance.RenameSQLite("current_streams")
		if err != nil {
			return fmt.Errorf("Error renaming next_streams to current_streams: %v\n", err)
		}

		db = newInstance
		return nil
	}

	tempName := fmt.Sprintf("temp_%d", time.Now().UnixNano())

	err := db.RenameSQLite(tempName)
	if err != nil {
		return fmt.Errorf("Error renaming current_streams to temp: %v\n", err)
	}

	err = newInstance.RenameSQLite("current_streams")
	if err != nil {
		revertErr := db.RenameSQLite("current_streams")
		if revertErr != nil {
			return fmt.Errorf("Error renaming back to current_streams: %v\n", revertErr)
		}
		return fmt.Errorf("Error renaming next_streams to current_streams: %v\n", err)
	}

	err = db.DeleteSQLite()
	if err != nil {
		fmt.Printf("Error deleting temp database: %v\n", err)
	}

	db = newInstance

	return nil
}

func updateSource(nextDb *database.Instance, m3uUrl string, index int) {
	log.Printf("Background process: Updating M3U #%d from %s\n", index, m3uUrl)
	err := m3u.ParseM3UFromURL(nextDb, m3uUrl, index)
	if err != nil {
		log.Printf("Background process: Error updating M3U: %v\n", err)
	} else {
		log.Printf("Background process: Updated M3U #%d from %s\n", index, m3uUrl)
	}
}

func updateSources(ctx context.Context) {
	// Ensure only one job is running at a time
	cronMutex.Lock()
	defer cronMutex.Unlock()

	select {
	case <-ctx.Done():
		return
	default:
		var err error
		nextDb, err := database.InitializeSQLite("next_streams")
		if err != nil {
			log.Fatalf("Error initializing next SQLite database: %v", err)
		}

		log.Println("Background process: Checking M3U_URLs...")
		var wg sync.WaitGroup
		index := 1
		for {
			m3uUrl, m3uExists := os.LookupEnv(fmt.Sprintf("M3U_URL_%d", index))
			if !m3uExists {
				break
			}

			log.Printf("Background process: Fetching M3U_URL_%d...\n", index)
			wg.Add(1)
			// Start the goroutine for periodic updates
			go func(nextDb *database.Instance, m3uUrl string, index int) {
				defer wg.Done()
				updateSource(nextDb, m3uUrl, index)
			}(nextDb, m3uUrl, index)

			index++
		}
		wg.Wait()

		err = swapDb(nextDb)
		if err != nil {
			log.Fatalf("swapDb: %v", err)
		}
		log.Println("Background process: Updated M3U database.")
	}
}

func main() {
	// Context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// manually set time zone
	if tz := os.Getenv("TZ"); tz != "" {
		var err error
		time.Local, err = time.LoadLocation(tz)
		if err != nil {
			log.Printf("error loading location '%s': %v\n", tz, err)
		}
	}

	var err error
	db, err = database.InitializeSQLite("current_streams")
	if err != nil {
		log.Fatalf("Error initializing current SQLite database: %v", err)
	}

	err = database.InitializeMemDB()
	if err != nil {
		log.Fatalf("Error initializing current memory database: %v", err)
	}

	cronSched := os.Getenv("SYNC_CRON")
	if len(strings.TrimSpace(cronSched)) == 0 {
		log.Println("SYNC_CRON not initialized. Defaulting to 0 0 * * * (12am every day).")
		cronSched = "0 0 * * *"
	}

	c := cron.New()
	_, err = c.AddFunc(cronSched, func() {
		go updateSources(ctx)
	})
	if err != nil {
		log.Fatalf("Error initializing background processes: %v", err)
	}
	c.Start()

	syncOnBoot := os.Getenv("SYNC_ON_BOOT")
	if len(strings.TrimSpace(syncOnBoot)) == 0 {
		syncOnBoot = "true"
	}

	if syncOnBoot == "true" {
		log.Println("SYNC_ON_BOOT enabled. Starting initial M3U update.")
		go updateSources(ctx)
	}

	// HTTP handlers
	http.HandleFunc("/playlist.m3u", func(w http.ResponseWriter, r *http.Request) {
		swappingLock.Lock()
		defer swappingLock.Unlock()

		m3u.GenerateM3UContent(w, r, db)
	})
	http.HandleFunc("/stream/", func(w http.ResponseWriter, r *http.Request) {
		mp4Handler(w, r, db)
	})

	// Start the server
	log.Println("Server is running on port 8080...")
	log.Println("Playlist Endpoint is running (`/playlist.m3u`)")
	log.Println("Stream Endpoint is running (`/stream/{streamID}.mp4`)")
	err = http.ListenAndServe(":8080", nil)
	if err != nil {
		log.Fatalf("HTTP server error: %v", err)
	}
}
