package main

import (
	"context"
	"fmt"
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
		utils.SafeLogFatalf("Error initializing Redis database: %v", err)
	}

	utils.SafeLogln("Starting updater...")
	_, err = updater.Initialize(ctx)
	if err != nil {
		utils.SafeLogFatalf("Error initializing updater: %v", err)
	}

	// manually set time zone
	if tz := os.Getenv("TZ"); tz != "" {
		var err error
		time.Local, err = time.LoadLocation(tz)
		if err != nil {
			utils.SafeLogf("error loading location '%s': %v\n", tz, err)
		}
	}

	utils.SafeLogln("Clearing stale concurrency data from database...")
	err = db.ClearConcurrencies()
	if err != nil {
		utils.SafeLogFatalf("Error clearing concurrency database: %v", err)
	}

	utils.SafeLogln("Setting up HTTP handlers...")
	// HTTP handlers
	http.HandleFunc("/playlist.m3u", func(w http.ResponseWriter, r *http.Request) {
		m3u.Handler(w, r)
	})
	http.HandleFunc("/stream/", func(w http.ResponseWriter, r *http.Request) {
		proxy.Handler(w, r)
	})

	customPathsByGroup := utils.GetCustomPathsByGroup()
	for path := range customPathsByGroup {
		http.HandleFunc(fmt.Sprintf("/%s/", path), func(w http.ResponseWriter, r *http.Request) {
			proxy.Handler(w, r)
		})
	}

	customPathsByTitle := utils.GetCustomPathsByTitle()
	for path := range customPathsByTitle {
		http.HandleFunc(fmt.Sprintf("/%s/", path), func(w http.ResponseWriter, r *http.Request) {
			proxy.Handler(w, r)
		})
	}

	// Start the server
	utils.SafeLogln("Server is running on port 8080...")
	utils.SafeLogln("Playlist Endpoint is running (`/playlist.m3u`)")
	utils.SafeLogln("Stream Endpoint is running (`/stream/{streamID}.{fileExt}`)")
	for path := range customPathsByGroup {
		utils.SafeLogf("Stream Endpoint is running (`/%s/{streamID}.{fileExt}`)\n", path)
	}
	for path := range customPathsByTitle {
		utils.SafeLogf("Stream Endpoint is running (`/%s/{streamID}.{fileExt}`)\n", path)
	}
	err = http.ListenAndServe(":8080", nil)
	if err != nil {
		utils.SafeLogFatalf("HTTP server error: %v", err)
	}
}
