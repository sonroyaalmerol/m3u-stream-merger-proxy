package main

import (
	"errors"
	"io"
	"log"
	"m3u-stream-merger/database"
	"m3u-stream-merger/utils"
	"net/http"
	"strings"
	"sync"
	"syscall"
)

func mp4Handler(w http.ResponseWriter, r *http.Request) {
	// Log the incoming request
	log.Printf("Received request from %s for URL: %s\n", r.RemoteAddr, r.URL.Path)

	// Extract the m3u ID from the URL path
	m3uID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/stream/"), ".mp4")
	if m3uID == "" {
		http.NotFound(w, r)
		return
	}

	streamName := utils.GetStreamName(m3uID)
	if streamName == "" {
		http.NotFound(w, r)
		return
	}

	stream, err := database.GetStreamByTitle(streamName)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// You can modify the response header as needed
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	var resp *http.Response
	var mutex sync.Mutex
	defer func() {
		mutex.Lock()
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		mutex.Unlock()
	}()

	// Concurrently handle each stream URL
	var wg sync.WaitGroup
	for _, url := range stream.URLs {
		wg.Add(1)
		go func(url database.StreamURL) {
			defer wg.Done()

			// Ensure that the maximum concurrency is respected
			semaphore := make(chan struct{}, url.MaxConcurrency)
			semaphore <- struct{}{}

			mutex.Lock()
			defer mutex.Unlock()

			// If there's already a successful response, return immediately
			if resp != nil {
				return
			}

			// Wait for permission from the semaphore before making the request
			<-semaphore
			defer func() {
				semaphore <- struct{}{}
			}()

			// Make the request
			response, err := http.Get(url.Content)
			if err != nil {
				// Log the error
				log.Printf("Error fetching MP4 stream: %s\n", err.Error())
				return
			}

			// Set the response if successful
			resp = response
		}(url)
	}

	if resp == nil {
		// Log the error
		log.Println("Error fetching MP4 stream. Exhausted all streams.")
		// Check if the connection is still open before writing to the response
		select {
		case <-r.Context().Done():
			// Connection closed, handle accordingly
			log.Println("Client disconnected")
			return
		default:
			// Connection still open, proceed with writing to the response
			http.Error(w, "Error fetching MP4 stream. Exhausted all streams.", http.StatusInternalServerError)
			return
		}
	}

	// Log the successful response
	log.Printf("Sent MP4 stream to %s\n", r.RemoteAddr)

	// Check if the connection is still open before copying the MP4 stream to the response
	select {
	case <-r.Context().Done():
		// Connection closed, handle accordingly
		log.Println("Client disconnected after fetching MP4 stream")
		return
	default:
		// Connection still open, proceed with writing to the response
		_, err := io.Copy(w, resp.Body)
		if err != nil {
			// Log the error
			if errors.Is(err, syscall.EPIPE) {
				log.Println("Client disconnected after fetching MP4 stream")
			} else {
				log.Printf("Error copying MP4 stream to response: %s\n", err.Error())
			}
			return
		}
	}
}
