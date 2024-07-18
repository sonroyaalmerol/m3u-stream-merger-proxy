package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"m3u-stream-merger/database"
	"m3u-stream-merger/utils"
	"net/http"
	"os"
	"strconv"
	"strings"
)

func loadBalancer(stream database.StreamInfo) (*http.Response, *database.StreamURL, error) {
	loadBalancingMode := os.Getenv("LOAD_BALANCING_MODE")
	if loadBalancingMode == "" {
		loadBalancingMode = "brute-force"
	}

	var lastIndex int

	for i := 0; i < len(stream.URLs); i++ {
		index := i
		if loadBalancingMode == "round-robin" {
			index = (lastIndex + i) % len(stream.URLs)
		}

		url := stream.URLs[index]

		if checkConcurrency(url.M3UIndex) {
			log.Printf("Concurrency limit reached for M3U_%d: %s", url.M3UIndex, url.Content)
			continue
		}

		resp, err := utils.CustomHttpRequest("GET", url.Content)
		if err == nil {
			return resp, &url, nil
		}
		log.Printf("Error fetching stream: %s\n", err.Error())

		if loadBalancingMode == "round-robin" {
			lastIndex = (lastIndex + 1) % len(stream.URLs)
		}
	}

	log.Printf("All concurrency limits have been reached. Falling back to connection checking mode...\n")
	for _, url := range stream.URLs {
		resp, err := utils.CustomHttpRequest("GET", url.Content)
		if err == nil {
			return resp, &url, nil
		}
		log.Printf("Error fetching stream: %s\n", err.Error())
	}

	return nil, nil, fmt.Errorf("Error fetching stream. Exhausted all streams.")
}

func proxyStream(selectedUrl *database.StreamURL, resp *http.Response, r *http.Request, w http.ResponseWriter, statusChan chan int) {
	updateConcurrency(selectedUrl.M3UIndex, true)
	defer updateConcurrency(selectedUrl.M3UIndex, false)

	bufferMbInt, _ := strconv.Atoi(os.Getenv("BUFFER_MB"))
	if bufferMbInt < 0 {
		log.Printf("Invalid BUFFER_MB value: negative integer is not allowed\n")
		bufferMbInt = 0
	}
	buffer := make([]byte, 1024)
	if bufferMbInt > 0 {
		buffer = make([]byte, bufferMbInt*1024*1024)
	}

	for {
		n, err := resp.Body.Read(buffer)
		if err != nil {
			if err == io.EOF {
				log.Printf("Stream ended (EOF reached): %s\n", r.RemoteAddr)
				statusChan <- 1
				return
			}
			log.Printf("Error reading stream: %s\n", err.Error())
			statusChan <- 1
			return
		}
		if _, err := w.Write(buffer[:n]); err != nil {
			log.Printf("Error writing to response: %s\n", err.Error())
			statusChan <- 0
			return
		}
	}
}

func streamHandler(w http.ResponseWriter, r *http.Request, db *database.Instance) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	log.Printf("Received request from %s for URL: %s\n", r.RemoteAddr, r.URL.Path)

	m3uID := strings.Split(strings.TrimPrefix(r.URL.Path, "/stream/"), ".")[0]
	if m3uID == "" {
		http.NotFound(w, r)
		return
	}

	streamName := utils.GetStreamName(m3uID)
	if streamName == "" {
		http.NotFound(w, r)
		return
	}

	stream, err := db.GetStreamByTitle(streamName)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	resp, selectedUrl, err := loadBalancer(stream)
	if err != nil {
		http.Error(w, "Error fetching stream. Exhausted all streams.", http.StatusInternalServerError)
		return
	}

	log.Printf("Proxying %s to %s\n", r.RemoteAddr, selectedUrl.Content)

	for k, v := range resp.Header {
		for _, val := range v {
			w.Header().Set(k, val)
		}
	}

	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	for {
		select {
		case <-ctx.Done():
			log.Printf("Client disconnected: %s\n", r.RemoteAddr)
			resp.Body.Close()
		default:
			exitStatus := make(chan int)
			go proxyStream(selectedUrl, resp, r, w, exitStatus)
			streamExitCode := <-exitStatus

			if streamExitCode == 1 {
				// Retry on server-side connection errors
				log.Printf("Server connection failed: %s\n", selectedUrl.Content)
				log.Printf("Retrying other servers...\n")
				resp.Body.Close()
				resp, selectedUrl, err = loadBalancer(stream)
				if err != nil {
					http.Error(w, "Error fetching stream. Exhausted all streams.", http.StatusInternalServerError)
					return
				}
				log.Printf("Reconnected to %s\n", selectedUrl.Content)
			} else {
				// Consider client-side connection errors as complete closure
				log.Printf("Client has closed the stream: %s\n", r.RemoteAddr)
				cancel()
				break
			}
		}
	}
}

func checkConcurrency(m3uIndex int) bool {
	maxConcurrency, err := strconv.Atoi(os.Getenv(fmt.Sprintf("M3U_MAX_CONCURRENCY_%d", m3uIndex)))
	if err != nil {
		maxConcurrency = 1
	}

	count, err := database.GetConcurrency(m3uIndex)
	if err != nil {
		log.Printf("Error checking concurrency: %s\n", err.Error())
		return false
	}

	log.Printf("Current number of connections for M3U_%d: %d", m3uIndex, count)
	return count >= maxConcurrency
}

func updateConcurrency(m3uIndex int, incr bool) {
	var err error
	if incr {
		err = database.IncrementConcurrency(m3uIndex)
	} else {
		err = database.DecrementConcurrency(m3uIndex)
	}
	if err != nil {
		log.Printf("Error updating concurrency: %s\n", err.Error())
	}

	count, err := database.GetConcurrency(m3uIndex)
	if err != nil {
		log.Printf("Error checking concurrency: %s\n", err.Error())
	}
	log.Printf("Current number of connections for M3U_%d: %d", m3uIndex, count)
}
