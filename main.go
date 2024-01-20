package main

import (
	"fmt"
	"io"
	"log"
	"m3u-stream-merger/m3u"
	"m3u-stream-merger/utils"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
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

	stream, err := m3u.FindStreamByName(streamName)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// You can modify the response header as needed
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	var resp *http.Response

	for i, url := range stream.URLs {
		resp, err := http.Get(url.Content)
		if err != nil {
			if i < len(stream.URLs) {
				continue
			}
			// Log the error
			log.Printf("Error fetching MP4 stream, exhausted all streams: %s\n", err.Error())
			// Check if the connection is still open before writing to the response
			select {
			case <-r.Context().Done():
				// Connection closed, handle accordingly
				log.Println("Client disconnected")
				return
			default:
				// Connection still open, proceed with writing to the response
				http.Error(w, "Error fetching MP4 stream. Exhausted all streams.", http.StatusInternalServerError)
			}
			return
		}

		defer resp.Body.Close()
		break
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
			log.Printf("Error copying MP4 stream to response: %s\n", err.Error())
			http.Error(w, "Error copying MP4 stream to response", http.StatusInternalServerError)
			return
		}
	}

	// Sleep for a short duration to allow the server to detect the client disconnection
	time.Sleep(100 * time.Millisecond)

	// Explicitly check if the connection is still open after a short delay
	select {
	case <-r.Context().Done():
		log.Printf("Connection still open after copying MP4 stream. The client may have disconnected.")
	default:
		// Connection still open after the delay
	}
}

func updateSource() {
	for {
		updateIntervalInHour, exists := os.LookupEnv("UPDATE_INTERVAL")
		if !exists {
			updateIntervalInHour = "24"
		}

		hourInt, err := strconv.Atoi(updateIntervalInHour)
		if err != nil {
			time.Sleep(24 * time.Hour)
		} else {
			time.Sleep(time.Duration(hourInt) * time.Hour) // Adjust the update interval as needed
		}

		// Reload M3U files
		err = m3u.GetStreams(true)
		if err != nil {
			fmt.Printf("Error updating M3U: %v\n", err)
		}
	}
}

func main() {
	// Initial load of M3U files
	err := m3u.GetStreams(false)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	// Start the goroutine for periodic updates
	go updateSource()

	// HTTP handlers
	http.HandleFunc("/playlist.m3u", m3u.GenerateM3UContent)
	http.HandleFunc("/stream/", mp4Handler)

	// Start the server
	fmt.Println("Server is running on port 8080...")
	http.ListenAndServe(":8080", nil)
}
