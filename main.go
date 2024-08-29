package main

import (
	"context"
	"m3u-stream-merger/database"
	"m3u-stream-merger/m3u"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/updater"
	"m3u-stream-merger/utils"
	"net/http"
	"os"
	"time"
)

func main() {
	// Context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	utils.SafeLogln("Checking database connection...")
	db, err := database.InitializeDb()
	if err != nil {
		utils.SafeLogFatal("Error initializing Redis database: %v", err)
	}

	utils.SafeLogln("Starting updater...")
	_, err = updater.Initialize(ctx)
	if err != nil {
		utils.SafeLogFatal("Error initializing updater: %v", err)
	}

	// manually set time zone
	if tz := os.Getenv("TZ"); tz != "" {
		var err error
		time.Local, err = time.LoadLocation(tz)
		if err != nil {
			utils.SafeLog("error loading location '%s': %v\n", tz, err)
		}
	}

	utils.SafeLogln("Clearing stale concurrency data from database...")
	err = db.ClearConcurrencies()
	if err != nil {
		utils.SafeLogFatal("Error clearing concurrency database: %v", err)
	}

	utils.SafeLogln("Setting up HTTP handlers...")
	// HTTP handlers
	http.HandleFunc("/playlist.m3u", func(w http.ResponseWriter, r *http.Request) {
		m3u.Handler(w, r)
	})
	http.HandleFunc("/stream/", func(w http.ResponseWriter, r *http.Request) {
		proxy.Handler(w, r)
	})

	// Start the server
	utils.SafeLogln("Server is running on port 8080...")
	utils.SafeLogln("Playlist Endpoint is running (`/playlist.m3u`)")
	utils.SafeLogln("Stream Endpoint is running (`/stream/{streamID}.{fileExt}`)")
	err = http.ListenAndServe(":8080", nil)
	if err != nil {
		utils.SafeLogFatal("HTTP server error: %v", err)
	}
}
