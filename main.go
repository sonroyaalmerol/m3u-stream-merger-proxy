package main

import (
	"context"
	"fmt"
	"log"
	"m3u-stream-merger/database"
	"m3u-stream-merger/m3u"
	"maps"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

var db *database.Instance
var cronMutex sync.Mutex

func updateSource(tmpStore map[string]database.StreamInfo, m3uUrl string, index int) {
	log.Printf("Background process: Updating M3U #%d from %s\n", index+1, m3uUrl)
	err := m3u.ParseM3UFromURL(tmpStore, m3uUrl, index)
	if err != nil {
		log.Printf("Background process: Error updating M3U: %v\n", err)
	} else {
		log.Printf("Background process: Updated M3U #%d from %s\n", index+1, m3uUrl)
	}

}

func updateSources(ctx context.Context, ewg *sync.WaitGroup) {
	// Ensure only one job is running at a time
	cronMutex.Lock()
	defer cronMutex.Unlock()
	if ewg != nil {
		defer ewg.Done()
	}

	select {
	case <-ctx.Done():
		return
	default:
		log.Println("Background process: Checking M3U_URLs...")
		var wg sync.WaitGroup
		index := 0
		tmpStore := map[string]database.StreamInfo{}

		for {
			m3uUrl, m3uExists := os.LookupEnv(fmt.Sprintf("M3U_URL_%d", index+1))
			if !m3uExists {
				break
			}

			log.Printf("Background process: Fetching M3U_URL_%d...\n", index+1)
			wg.Add(1)
			// Start the goroutine for periodic updates
			go func(store map[string]database.StreamInfo, m3uUrl string, index int) {
				defer wg.Done()
				updateSource(store, m3uUrl, index)
			}(tmpStore, m3uUrl, index)

			index++
		}
		wg.Wait()

		err := db.SaveToDb(slices.Collect(maps.Values(tmpStore)))
		if err != nil {
			log.Printf("Background process: Error updating M3U database: %v\n", err)
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

	cacheOnSync := os.Getenv("CACHE_ON_SYNC")
	if len(strings.TrimSpace(cacheOnSync)) == 0 {
		cacheOnSync = "false"
	}

	cronSched := os.Getenv("SYNC_CRON")
	if len(strings.TrimSpace(cronSched)) == 0 {
		log.Println("SYNC_CRON not initialized. Defaulting to 0 0 * * * (12am every day).")
		cronSched = "0 0 * * *"
	}

	c := cron.New()
	_, err = c.AddFunc(cronSched, func() {
		var wg sync.WaitGroup

		wg.Add(1)
		go updateSources(ctx, &wg)
		if cacheOnSync == "true" {
			if _, ok := os.LookupEnv("BASE_URL"); !ok {
				log.Println("BASE_URL is required for CACHE_ON_SYNC to work.")
			}
			wg.Wait()
			log.Println("CACHE_ON_SYNC enabled. Building cache.")
			m3u.InitCache(db)
		}
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

		var wg sync.WaitGroup

		wg.Add(1)
		go updateSources(ctx, &wg)
		if cacheOnSync == "true" {
			if _, ok := os.LookupEnv("BASE_URL"); !ok {
				log.Println("BASE_URL is required for CACHE_ON_SYNC to work.")
			}
			wg.Wait()
			log.Println("CACHE_ON_SYNC enabled. Building cache.")
			m3u.InitCache(db)
		}
	}

	// HTTP handlers
	http.HandleFunc("/playlist.m3u", func(w http.ResponseWriter, r *http.Request) {
		m3u.Handler(w, r, db)
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
