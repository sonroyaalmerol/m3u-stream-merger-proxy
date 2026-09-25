package main

import (
	"context"
	"m3u-stream-merger/handlers"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/updater"
	"net/http"
	"net/url"
	"os"
	"time"
)

// redactQuery hides the password before a request line reaches the log.
func redactQuery(raw string) string {
	q, err := url.ParseQuery(raw)
	if err != nil {
		return "<unparseable>"
	}
	if q.Get("password") != "" {
		q.Set("password", "***")
	}
	return q.Encode()
}

func requestLine(r *http.Request) string {
	line := r.Method + " " + r.URL.Path
	if r.URL.RawQuery != "" {
		line += "?" + redactQuery(r.URL.RawQuery)
	}
	return line + " ua=\"" + r.UserAgent() + "\" from=" + r.RemoteAddr
}

// logXtream logs every Xtream client request so player compatibility issues are diagnosable from the logs alone.
func logXtream(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logger.Default.Logf("xtream: %s", requestLine(r))
		next(w, r)
	}
}

func rootHandler(serveAPI func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path == "/" && (q.Get("action") != "" || (q.Get("username") != "" && q.Get("password") != "")) {
			logger.Default.Logf("xtream: %s", requestLine(r))
			serveAPI(w, r)
			return
		}
		logger.Default.Warnf("unhandled request (404): %s", requestLine(r))
		http.NotFound(w, r)
	}
}

func main() {
	applyMemoryLimit()

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
	http.HandleFunc("/player_api.php", logXtream(xtreamHandler.ServePlayerAPI))
	http.HandleFunc("/get.php", logXtream(xtreamHandler.ServeGetPHP))
	http.HandleFunc("/xmltv.php", logXtream(xtreamHandler.ServeXMLTV))
	http.HandleFunc("/panel_api.php", logXtream(xtreamHandler.ServePanelAPI))
	for _, prefix := range []string{"/live/", "/movie/", "/series/"} {
		http.HandleFunc(prefix, logXtream(xtreamHandler.ServeStream))
	}

	http.HandleFunc("/", rootHandler(xtreamHandler.ServePlayerAPI))

	logger.Default.Logf("Server is running on port %s...", os.Getenv("PORT"))
	logger.Default.Log("Playlist Endpoint is running (`/playlist.m3u`)")
	logger.Default.Log("Stream Endpoint is running (`/p/{originalBasePath}/{streamID}.{fileExt}`)")
	logger.Default.Log("EPG Endpoint is running (`/epg.xml`)")
	logger.Default.Log("Xtream API is running (`/player_api.php`, `/panel_api.php`, `/live|movie|series/{user}/{pass}/{id}.{ext}`, `/get.php`, `/xmltv.php`)")
	setup, err := newTLSSetup(logger.Default)
	if err != nil {
		logger.Default.Fatalf("TLS setup error: %v", err)
	}
	if err := setup.serve(); err != nil {
		logger.Default.Fatalf("HTTP server error: %v", err)
	}
}
