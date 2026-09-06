package main

import (
	"context"
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

	m3uHandler := handlers.NewM3UHTTPHandler(logger.Default, "")
	epgHandler := handlers.NewEPGHTTPHandler(m3uHandler)
	streamHandler := handlers.NewStreamHTTPHandler(handlers.NewDefaultProxyInstance(), logger.Default)
	passthroughHandler := handlers.NewPassthroughHTTPHandler(logger.Default)
	xtreamHandler := handlers.NewXtreamHTTPHandler(streamHandler, logger.Default)

	logger.Default.Log("Starting updater...")
	_, err := updater.Initialize(ctx, logger.Default, m3uHandler, epgHandler)
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

	logger.Default.Log("Setting up HTTP handlers...")
	// HTTP handlers
	http.HandleFunc("/playlist.m3u", func(w http.ResponseWriter, r *http.Request) {
		m3uHandler.ServeHTTP(w, r)
	})
	http.HandleFunc("/p/", func(w http.ResponseWriter, r *http.Request) {
		streamHandler.ServeHTTP(w, r)
	})
	http.HandleFunc("/a/", func(w http.ResponseWriter, r *http.Request) {
		passthroughHandler.ServeHTTP(w, r)
	})
	http.HandleFunc("/segment/", func(w http.ResponseWriter, r *http.Request) {
		streamHandler.ServeSegmentHTTP(w, r)
	})
	http.HandleFunc("/epg.xml", func(w http.ResponseWriter, r *http.Request) {
		epgHandler.ServeHTTP(w, r)
	})
	http.HandleFunc("/player_api.php", func(w http.ResponseWriter, r *http.Request) {
		xtreamHandler.ServePlayerAPI(w, r)
	})
	http.HandleFunc("/get.php", func(w http.ResponseWriter, r *http.Request) {
		xtreamHandler.ServeGetPHP(w, r)
	})
	http.HandleFunc("/xmltv.php", func(w http.ResponseWriter, r *http.Request) {
		xtreamHandler.ServeXMLTV(w, r)
	})
	for _, prefix := range []string{"/live/", "/movie/", "/series/"} {
		http.HandleFunc(prefix, func(w http.ResponseWriter, r *http.Request) {
			xtreamHandler.ServeStream(w, r)
		})
	}

	// Start the server
	logger.Default.Logf("Server is running on port %s...", os.Getenv("PORT"))
	logger.Default.Log("Playlist Endpoint is running (`/playlist.m3u`)")
	logger.Default.Log("Stream Endpoint is running (`/p/{originalBasePath}/{streamID}.{fileExt}`)")
	logger.Default.Log("EPG Endpoint is running (`/epg.xml`)")
	logger.Default.Log("Xtream API is running (`/player_api.php`, `/live|movie|series/{user}/{pass}/{id}.{ext}`, `/get.php`)")
	setup, err := newTLSSetup(logger.Default)
	if err != nil {
		logger.Default.Fatalf("TLS setup error: %v", err)
	}
	if err := setup.serve(); err != nil {
		logger.Default.Fatalf("HTTP server error: %v", err)
	}
}
