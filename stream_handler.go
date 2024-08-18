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
	"sort"
	"strconv"
	"strings"
)

func loadBalancer(stream database.StreamInfo) (*http.Response, *database.StreamURL, error) {
	sort.Slice(stream.URLs, func(i, j int) bool {
		return db.ConcurrencyPriorityValue(stream.URLs[i].M3UIndex) > db.ConcurrencyPriorityValue(stream.URLs[j].M3UIndex)
	})

	const maxLaps = 5
	lap := 0
	index := 0
	for lap < maxLaps {
		if index >= len(stream.URLs) {
			index = 0
			lap++
			if lap >= maxLaps {
				break
			}
		}

		url := stream.URLs[index]
		index++

		if db.CheckConcurrency(url.M3UIndex) {
			log.Printf("Concurrency limit reached for M3U_%d: %s", url.M3UIndex+1, url.Content)
			continue
		}

		resp, err := utils.CustomHttpRequest("GET", url.Content)
		if err == nil {
			return resp, &url, nil
		}
		log.Printf("Error fetching stream: %s\n", err.Error())
	}

	return nil, nil, fmt.Errorf("Error fetching stream. Exhausted all streams.")
}

func proxyStream(selectedUrl *database.StreamURL, resp *http.Response, r *http.Request, w http.ResponseWriter, statusChan chan int) {
	db.UpdateConcurrency(selectedUrl.M3UIndex, true)
	defer db.UpdateConcurrency(selectedUrl.M3UIndex, false)

	bufferMbInt, err := strconv.Atoi(os.Getenv("BUFFER_MB"))
	if err != nil || bufferMbInt < 0 {
		log.Printf("Invalid BUFFER_MB value: %v. Defaulting to 1KB buffer\n", err)
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

		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

func streamHandler(w http.ResponseWriter, r *http.Request, db *database.Instance) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	log.Printf("Received request from %s for URL: %s\n", r.RemoteAddr, r.URL.Path)

	streamUrl := strings.Split(strings.TrimPrefix(r.URL.Path, "/stream/"), ".")[0]
	if streamUrl == "" {
		log.Printf("Invalid m3uID for request from %s: %s\n", r.RemoteAddr, r.URL.Path)
		http.NotFound(w, r)
		return
	}

	streamSlug := utils.GetStreamSlugFromUrl(streamUrl)
	if streamSlug == "" {
		log.Printf("No stream found for streamUrl %s from %s\n", streamUrl, r.RemoteAddr)
		http.NotFound(w, r)
		return
	}

	stream, err := db.GetStreamBySlug(streamSlug)
	if err != nil {
		log.Printf("Error retrieving stream for slug %s: %v\n", streamSlug, err)
		http.NotFound(w, r)
		return
	}

	resp, selectedUrl, err := loadBalancer(stream)
	if err != nil {
		log.Printf("Error fetching initial stream for %s: %v\n", streamSlug, err)
		http.Error(w, "Error fetching stream. Exhausted all streams.", http.StatusInternalServerError)
		return
	}

	for k, v := range resp.Header {
		if strings.ToLower(k) != "content-length" {
			for _, val := range v {
				w.Header().Set(k, val)
			}
		}
	}

	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	for {
		select {
		case <-ctx.Done():
			log.Printf("Client disconnected: %s\n", r.RemoteAddr)
			resp.Body.Close()
			return
		default:
			exitStatus := make(chan int)

			currentUrl := selectedUrl
			currentResp := resp

			log.Printf("Proxying %s to %s\n", r.RemoteAddr, selectedUrl.Content)
			go func(url *database.StreamURL, resp *http.Response, r *http.Request, w http.ResponseWriter, exitStatus chan int) {
				proxyStream(url, resp, r, w, exitStatus)
			}(currentUrl, currentResp, r, w, exitStatus)

			streamExitCode := <-exitStatus
			log.Printf("Exit code %d received from %s\n", streamExitCode, selectedUrl.Content)

			if streamExitCode == 1 {
				// Retry on server-side connection errors
				log.Printf("Server connection failed: %s\n", selectedUrl.Content)
				log.Printf("Retrying other servers...\n")
				currentResp.Body.Close()
				resp, selectedUrl, err = loadBalancer(stream)
				if err != nil {
					log.Printf("Error reloading stream for %s: %v\n", streamSlug, err)
					http.Error(w, "Error fetching stream. Exhausted all streams.", http.StatusInternalServerError)
					return
				}
			} else {
				// Consider client-side connection errors as complete closure
				log.Printf("Client has closed the stream: %s\n", r.RemoteAddr)
				cancel()
			}
		}
	}
}
