package main

import (
	"context"
	"log"
	"m3u-stream-merger/database"
	"m3u-stream-merger/m3u"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/updater"
	"net/http"
	"os"
	"time"
)

func main() {
	// Context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log.Println("Checking database connection...")
	db, err := database.InitializeDb()
	if err != nil {
		log.Fatalf("Error initializing Redis database: %v", err)
	}

	log.Println("Starting updater...")
	updater.Initialize(ctx)

	// manually set time zone
	if tz := os.Getenv("TZ"); tz != "" {
		var err error
		time.Local, err = time.LoadLocation(tz)
		if err != nil {
			log.Printf("error loading location '%s': %v\n", tz, err)
		}
	}

	log.Println("Clearing stale concurrency data from database...")
	err = db.ClearConcurrencies()
	if err != nil {
		log.Fatalf("Error clearing concurrency database: %v", err)
	}

	log.Println("Setting up HTTP handlers...")
	// HTTP handlers
	http.HandleFunc("/playlist.m3u", func(w http.ResponseWriter, r *http.Request) {
		m3u.Handler(w, r)
	})
	http.HandleFunc("/stream/", func(w http.ResponseWriter, r *http.Request) {
		proxy.Handler(w, r)
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
