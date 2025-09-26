package main

import (
	"context"
	"fmt"
	"m3u-stream-merger/handlers"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/updater"
	"net/http"
	"os"
	"strings"
	"time"
)

// AcceptMiddleware gestiona els encapçalaments Accept per evitar errors 406
func AcceptMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acceptHeader := r.Header.Get("Accept")
		
		// Si no hi ha encapçalament Accept, establir un per defecte
		if acceptHeader == "" {
			r.Header.Set("Accept", "*/*")
		} else {
			// Normalitzar l'encapçalament Accept per assegurar compatibilitat
			acceptHeader = normalizeAcceptHeader(acceptHeader, r.URL.Path)
			r.Header.Set("Accept", acceptHeader)
		}
		
		next.ServeHTTP(w, r)
	})
}

// normalizeAcceptHeader normalitza l'encapçalament Accept segons el tipus de petició
func normalizeAcceptHeader(accept string, path string) string {
	// Per a fitxers M3U, assegurar que s'accepta text/plain i application/vnd.apple.mpegurl
	if strings.HasSuffix(path, ".m3u") || strings.Contains(path, "playlist") {
		if !strings.Contains(accept, "text/plain") && 
		   !strings.Contains(accept, "application/vnd.apple.mpegurl") &&
		   !strings.Contains(accept, "*/*") {
			accept = "text/plain, application/vnd.apple.mpegurl, " + accept
		}
	}
	
	// Per a streams de vídeo, assegurar que s'accepten formats de vídeo
	if strings.Contains(path, "/p/") || strings.Contains(path, "/segment/") {
		if !strings.Contains(accept, "video/") && !strings.Contains(accept, "*/*") {
			accept = "video/mp2t, video/mp4, application/vnd.apple.mpegurl, " + accept
		}
	}
	
	// Per a passthrough, mantenir flexibilitat
	if strings.Contains(path, "/a/") {
		if !strings.Contains(accept, "*/*") {
			accept = accept + ", */*"
		}
	}
	
	return accept
}

// wrapHandlerWithAcceptHeaders encapsula els handlers existents amb gestió d'Accept
func wrapHandlerWithAcceptHeaders(handler http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Aplicar el middleware d'Accept
		middlewareHandler := AcceptMiddleware(handler)
		middlewareHandler.ServeHTTP(w, r)
	}
}

func main() {
	// Context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m3uHandler := handlers.NewM3UHTTPHandler(logger.Default, "")
	streamHandler := handlers.NewStreamHTTPHandler(handlers.NewDefaultProxyInstance(), logger.Default)
	passthroughHandler := handlers.NewPassthroughHTTPHandler(logger.Default)

	logger.Default.Log("Starting updater...")
	_, err := updater.Initialize(ctx, logger.Default, m3uHandler)
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

	logger.Default.Log("Setting up HTTP handlers with Accept header support...")
	
	// HTTP handlers amb suport per Accept headers
	http.HandleFunc("/playlist.m3u", wrapHandlerWithAcceptHeaders(m3uHandler))
	http.HandleFunc("/p/", wrapHandlerWithAcceptHeaders(streamHandler))
	http.HandleFunc("/a/", wrapHandlerWithAcceptHeaders(passthroughHandler))
	http.HandleFunc("/segment/", func(w http.ResponseWriter, r *http.Request) {
		// Per segments, aplicar middleware i després el handler específic
		middlewareHandler := AcceptMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			streamHandler.ServeSegmentHTTP(w, r)
		}))
		middlewareHandler.ServeHTTP(w, r)
	})

	// Start the server
	logger.Default.Logf("Server is running on port %s...", os.Getenv("PORT"))
	logger.Default.Log("Playlist Endpoint is running (`/playlist.m3u`) with Accept header support")
	logger.Default.Log("Stream Endpoint is running (`/p/{originalBasePath}/{streamID}.{fileExt}`) with Accept header support")
	logger.Default.Log("Passthrough Endpoint is running (`/a/`) with Accept header support")
	logger.Default.Log("Segment Endpoint is running (`/segment/`) with Accept header support")
	
	err = http.ListenAndServe(fmt.Sprintf(":%s", os.Getenv("PORT")), nil)
	if err != nil {
		logger.Default.Fatalf("HTTP server error: %v", err)
	}
}
