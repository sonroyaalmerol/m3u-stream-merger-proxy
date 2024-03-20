package main

import (
	"context"
	"fmt"
	"log"
	"m3u-stream-merger/database"
	"m3u-stream-merger/utils"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func loadBalancer(stream database.StreamInfo) (resp *http.Response, selectedUrl *database.StreamURL, err error) {
	loadBalancingMode := os.Getenv("LOAD_BALANCING_MODE")
	if loadBalancingMode == "" {
		loadBalancingMode = "brute-force"
	}

	switch loadBalancingMode {
	case "round-robin":
		var lastIndex int // Track the last index used

		// Round-robin mode
		for i := 0; i < len(stream.URLs); i++ {
			index := (lastIndex + i) % len(stream.URLs) // Calculate the next index
			url := stream.URLs[index]

			if checkConcurrency(url.M3UIndex) {
				maxCon := os.Getenv(fmt.Sprintf("M3U_MAX_CONCURRENCY_%d", url.M3UIndex))
				if strings.TrimSpace(maxCon) == "" {
					maxCon = "1"
				}
				log.Printf("Concurrency limit reached for M3U_%d (max: %s): %s", url.M3UIndex, maxCon, url.Content)
				continue // Skip this stream if concurrency limit reached
			}

			resp, err = utils.CustomHttpRequest("GET", url.Content)
			if err == nil {
				selectedUrl = &url
				break
			}

			// Log the error
			log.Printf("Error fetching MP4 stream (concurrency round robin mode): %s\n", err.Error())

			lastIndex = (lastIndex + 1) % len(stream.URLs) // Update the last index used
		}
	case "brute-force":
		// Brute force mode
		for _, url := range stream.URLs {
			if checkConcurrency(url.M3UIndex) {
				maxCon := os.Getenv(fmt.Sprintf("M3U_MAX_CONCURRENCY_%d", url.M3UIndex))
				if strings.TrimSpace(maxCon) == "" {
					maxCon = "1"
				}
				log.Printf("Concurrency limit reached for M3U_%d (max: %s): %s", url.M3UIndex, maxCon, url.Content)
				continue // Skip this stream if concurrency limit reached
			}

			resp, err = utils.CustomHttpRequest("GET", url.Content)
			if err == nil {
				selectedUrl = &url
				break
			}

			// Log the error
			log.Printf("Error fetching MP4 stream (concurrency brute force mode): %s\n", err.Error())
		}
	default:
		log.Printf("Invalid LOAD_BALANCING_MODE. Skipping concurrency mode...")
	}

	if selectedUrl == nil {
		log.Printf("All concurrency limits have been reached. Falling back to connection checking mode...\n")
		// Connection check mode
		for _, url := range stream.URLs {
			resp, err = utils.CustomHttpRequest("GET", url.Content)
			if err == nil {
				selectedUrl = &url
				break
			} else {
				// Log the error
				log.Printf("Error fetching MP4 stream (connection check mode): %s\n", err.Error())
			}
		}

		if resp == nil {
			// Log the error
			return nil, nil, fmt.Errorf("Error fetching MP4 stream. Exhausted all streams.")
		}

		return resp, selectedUrl, nil
	}

	return resp, selectedUrl, nil
}

func mp4Handler(w http.ResponseWriter, r *http.Request, db *database.Instance) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

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

	stream, err := db.GetStreamByTitle(streamName)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// You can modify the response header as needed
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	var resp *http.Response
	defer func() {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
	}()

	// Iterate through the streams and select one based on concurrency and availability
	var selectedUrl *database.StreamURL

	resp, selectedUrl, err = loadBalancer(stream)
	if err != nil {
		http.Error(w, "Error fetching MP4 stream. Exhausted all streams.", http.StatusInternalServerError)
		return
	}
	log.Printf("Proxying %s to %s\n", r.RemoteAddr, selectedUrl.Content)

	// Log the successful response
	log.Printf("Sent MP4 stream to %s\n", r.RemoteAddr)

	// Set initial timer duration
	timerDuration := 15 * time.Second
	timer := time.NewTimer(timerDuration)

	// Function to reset the timer
	resetTimer := func() {
		timer.Reset(timerDuration)
	}

	go func() {
		updateConcurrency(selectedUrl.M3UIndex, true)

		bufferMbInt := 0
		bufferMb := os.Getenv("BUFFER_MB")
		if bufferMb != "" {
			bufferMbInt, err = strconv.Atoi(bufferMb)
			if err != nil {
				log.Printf("Invalid BUFFER_MB value: %s\n", err.Error())
				bufferMbInt = 0
			}

			if bufferMbInt < 0 {
				log.Printf("Invalid BUFFER_MB value: negative integer is not allowed\n")
			}
		}

		buffer := make([]byte, 512)
		if bufferMbInt > 0 {
			log.Printf("Buffer is set to %dmb.\n", bufferMbInt)
			buffer = make([]byte, 1024*bufferMbInt)
		}
		for {
			select {
			case <-timer.C:
				log.Printf("Timer expired, closing connection for %s\n", r.RemoteAddr)
				cancel()
				return
			default:
				n, err := resp.Body.Read(buffer)
				if err != nil {
					log.Printf("Error reading MP4 stream: %s\n", err.Error())
					cancel()
					break
				}
				if n > 0 {
					resetTimer()
					_, err := w.Write(buffer[:n])
					if err != nil {
						log.Printf("Error writing to response: %s\n", err.Error())
						cancel()
						break
					}
				}
			}
		}
	}()

	// Wait for the request context to be canceled or the stream to finish
	<-ctx.Done()
	log.Printf("Client (%s) disconnected.\n", r.RemoteAddr)
	updateConcurrency(selectedUrl.M3UIndex, false)
}

func checkConcurrency(m3uIndex int) bool {
	maxConcurrency := 1
	var err error
	rawMaxConcurrency, maxConcurrencyExists := os.LookupEnv(fmt.Sprintf("M3U_MAX_CONCURRENCY_%d", m3uIndex))
	if maxConcurrencyExists {
		maxConcurrency, err = strconv.Atoi(rawMaxConcurrency)
		if err != nil {
			maxConcurrency = 1
		}
	}

	count, err := database.GetConcurrency(m3uIndex)
	if err != nil {
		log.Printf("Error checking concurrency: %s\n", err.Error())
		return false // Error occurred, treat as concurrency not reached
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
