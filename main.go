package main

import (
	"context"
	"fmt"
	"m3u-stream-merger/handlers"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/updater"
	"net/http"
	"os"
	"time"
)

func main() {
	// Context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger.Default.Log("Starting updater...")
	_, err := updater.Initialize(ctx)
	if err != nil {
		logger.Default.Fatalf("Error initializing updater: %v", err)
	}

	// manually set time zone
	if tz := os.Getenv("TZ"); tz != "" {
		var err error
		time.Local, err = time.LoadLocation(tz)
		if err != nil {
			logger.Default.Fatalf("error loading location '%s': %v\n", tz, err)
		}
	}

	m3uHandler := handlers.NewM3UHTTPHandler(logger.Default)
	streamHandler := handlers.NewStreamHTTPHandler(handlers.NewDefaultProxyInstance(), logger.Default)

	logger.Default.Log("Setting up HTTP handlers...")
	// HTTP handlers
	http.HandleFunc("/playlist.m3u", func(w http.ResponseWriter, r *http.Request) {
		m3uHandler.ServeHTTP(w, r)
	})
	http.HandleFunc("/p/", func(w http.ResponseWriter, r *http.Request) {
		streamHandler.ServeHTTP(w, r)
	})

	// Start the server
	logger.Default.Logf("Server is running on port %s...", os.Getenv("PORT"))
	logger.Default.Log("Playlist Endpoint is running (`/playlist.m3u`)")
	logger.Default.Log("Stream Endpoint is running (`/p/{originalBasePath}/{streamID}.{fileExt}`)")
	err = http.ListenAndServe(fmt.Sprintf(":%s", os.Getenv("PORT")), nil)
	if err != nil {
		logger.Default.Fatalf("HTTP server error: %v", err)
	}
}
