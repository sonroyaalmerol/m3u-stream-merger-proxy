package main

import (
	"context"
	"fmt"
	"m3u-stream-merger/handlers"
	"m3u-stream-merger/store"
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

	cm := store.NewConcurrencyManager()

	utils.SafeLogln("Starting updater...")
	_, err := updater.Initialize(ctx)
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

	utils.SafeLogln("Setting up HTTP handlers...")
	// HTTP handlers
	http.HandleFunc("/playlist.m3u", func(w http.ResponseWriter, r *http.Request) {
		handlers.M3UHandler(w, r)
	})
	http.HandleFunc("/p/", func(w http.ResponseWriter, r *http.Request) {
		handlers.StreamHandler(w, r, cm)
	})

	// Start the server
	utils.SafeLogln(fmt.Sprintf("Server is running on port %s...", os.Getenv("PORT")))
	utils.SafeLogln("Playlist Endpoint is running (`/playlist.m3u`)")
	utils.SafeLogln("Stream Endpoint is running (`/p/{originalBasePath}/{streamID}.{fileExt}`)")
	err = http.ListenAndServe(fmt.Sprintf(":%s", os.Getenv("PORT")), nil)
	if err != nil {
		utils.SafeLogFatalf("HTTP server error: %v", err)
	}
}
