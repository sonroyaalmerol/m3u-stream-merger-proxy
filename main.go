package main

import (
	"fmt"
	"io"
	"m3u-stream-merger/m3u"
	"m3u-stream-merger/utils"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func mp4Handler(w http.ResponseWriter, r *http.Request) {
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

	// Proxy the MP4 stream
	url := ""
	var urlUsed *m3u.StreamURL

	for _, u := range stream.URLs {
		if !u.Used {
			urlUsed = &u
			url = u.Content
			u.Used = true
			break
		}
	}

	if url == "" {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("Forbidden - No streams available."))
		return
	}

	resp, err := http.Get(url)
	if err != nil {
		urlUsed.Used = false
		http.Error(w, "Error fetching MP4 stream", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	// Copy the MP4 stream to the response writer
	_, err = io.Copy(w, resp.Body)
	if err != nil {
		urlUsed.Used = false
		http.Error(w, "Error copying MP4 stream to response", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	select {
	case <-ctx.Done():
		// change to unused
		urlUsed.Used = false
		return
	default:
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
	http.HandleFunc("/playlist", m3u.GenerateM3UContent)
	http.HandleFunc("/stream/", mp4Handler)

	// Start the server
	fmt.Println("Server is running on port 8080...")
	http.ListenAndServe(":8080", nil)
}
