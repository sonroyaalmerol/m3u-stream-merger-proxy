package main

import (
	"context"
	"fmt"
	"log"
	"m3u-stream-merger/database"
	"m3u-stream-merger/m3u"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

var db *database.Instance
var cronMutex sync.Mutex

func updateSource(nextDb *database.Instance, m3uUrl string, index int) {
	log.Printf("Background process: Updating M3U #%d from %s\n", index+1, m3uUrl)
	err := m3u.ParseM3UFromURL(nextDb, m3uUrl, index)
	if err != nil {
		log.Printf("Background process: Error updating M3U: %v\n", err)
	} else {
		log.Printf("Background process: Updated M3U #%d from %s\n", index+1, m3uUrl)
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
		log.Println("Background process: Checking M3U_URLs...")
		var wg sync.WaitGroup
		index := 0
		for {
			m3uUrl, m3uExists := os.LookupEnv(fmt.Sprintf("M3U_URL_%d", index+1))
			if !m3uExists {
				break
			}

			log.Printf("Background process: Fetching M3U_URL_%d...\n", index+1)
			wg.Add(1)
			// Start the goroutine for periodic updates
			go func(currDb *database.Instance, m3uUrl string, index int) {
				defer wg.Done()
				updateSource(currDb, m3uUrl, index)
			}(db, m3uUrl, index)

			index++
		}
		wg.Wait()

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

	REDIS_ADDR := os.Getenv("REDIS_ADDR")
	REDIS_PASS := os.Getenv("REDIS_PASS")
	REDIS_DB := 0
	if i, err := strconv.Atoi(os.Getenv("REDIS_DB")); err == nil {
		REDIS_DB = i
	}

	var err error
	db, err = database.InitializeDb(REDIS_ADDR, REDIS_PASS, REDIS_DB)
	if err != nil {
		log.Fatalf("Error initializing Redis database: %v", err)
	}

	err = db.ClearConcurrencies()
	if err != nil {
		log.Fatalf("Error clearing concurrency database: %v", err)
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

	clearOnBoot := os.Getenv("CLEAR_ON_BOOT")
	if len(strings.TrimSpace(clearOnBoot)) == 0 {
		clearOnBoot = "false"
	}

	if clearOnBoot == "true" {
		log.Println("CLEAR_ON_BOOT enabled. Clearing current database.")
		if err := db.ClearDb(); err != nil {
			log.Fatalf("Error clearing database: %v", err)
		}
	}

	// HTTP handlers
	http.HandleFunc("/playlist.m3u", func(w http.ResponseWriter, r *http.Request) {
		m3u.GenerateM3UContent(w, r, db)
	})
	http.HandleFunc("/stream/", func(w http.ResponseWriter, r *http.Request) {
		streamHandler(w, r, db)
	})

	// Start the server
	log.Println("Server is running on port 8080...")
	log.Println("Playlist Endpoint is running (`/playlist.m3u`)")
	log.Println("Stream Endpoint is running (`/stream/{streamID}.{fileExt}`)")
	err = http.ListenAndServe(":8080", nil)
	if err != nil {
		log.Fatalf("HTTP server error: %v", err)
	}
}
