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
	"slices"
	"sort"
	"strconv"
	"strings"
)

func loadBalancer(stream database.StreamInfo, previous []int) (*http.Response, string, int, error) {
	m3uIndexes := utils.GetM3UIndexes()

	sort.Slice(m3uIndexes, func(i, j int) bool {
		return db.ConcurrencyPriorityValue(i) > db.ConcurrencyPriorityValue(j)
	})

	const maxLaps = 5
	lap := 0

	for lap < maxLaps {
		allSkipped := true // Assume all URLs might be skipped

		for _, index := range m3uIndexes {
			if slices.Contains(previous, index) {
				log.Printf("Skipping M3U_%d: marked as previous stream\n", index+1)
				continue
			}

			url, ok := stream.URLs[index]
			if !ok {
				log.Printf("Channel not found from M3U_%d: %s\n", index+1, stream.Title)
				continue
			}

			if db.CheckConcurrency(index) {
				log.Printf("Concurrency limit reached for M3U_%d: %s\n", index+1, url)
				continue
			}

			allSkipped = false // At least one URL is not skipped

			resp, err := utils.CustomHttpRequest("GET", url)
			if err == nil {
				return resp, url, index, nil
			}
			log.Printf("Error fetching stream: %s\n", err.Error())
		}

		if allSkipped {
			break
		}

		lap++
	}

	return nil, "", -1, fmt.Errorf("Error fetching stream. Exhausted all streams.")
}

func proxyStream(m3uIndex int, resp *http.Response, r *http.Request, w http.ResponseWriter, statusChan chan int) {
	db.UpdateConcurrency(m3uIndex, true)
	defer db.UpdateConcurrency(m3uIndex, false)

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

	selectedIndex := -1
	selectedUrl := ""
	testedIndexes := []int{}

	var resp *http.Response

	for {
		select {
		case <-ctx.Done():
			log.Printf("Client disconnected: %s\n", r.RemoteAddr)
			if resp != nil {
				resp.Body.Close()
			}
			return
		default:
			resp, selectedUrl, selectedIndex, err = loadBalancer(stream, testedIndexes)
			if err != nil {
				log.Printf("Error reloading stream for %s: %v\n", streamSlug, err)
				http.Error(w, "Error fetching stream. Exhausted all streams.", http.StatusInternalServerError)
				return
			}

			// HTTP header initialization
			if selectedIndex == -1 {
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("Access-Control-Allow-Origin", "*")
				for k, v := range resp.Header {
					if strings.ToLower(k) != "content-length" {
						for _, val := range v {
							w.Header().Set(k, val)
						}
					}
				}
			}

			exitStatus := make(chan int)

			log.Printf("Proxying %s to %s\n", r.RemoteAddr, selectedUrl)
			go func(m3uIndex int, resp *http.Response, r *http.Request, w http.ResponseWriter, exitStatus chan int) {
				proxyStream(m3uIndex, resp, r, w, exitStatus)
			}(selectedIndex, resp, r, w, exitStatus)
			testedIndexes = append(testedIndexes, selectedIndex)

			streamExitCode := <-exitStatus
			log.Printf("Exit code %d received from %s\n", streamExitCode, selectedUrl)

			if streamExitCode == 1 {
				// Retry on server-side connection errors
				log.Printf("Retrying other servers...\n")
			} else {
				// Consider client-side connection errors as complete closure
				log.Printf("Client has closed the stream: %s\n", r.RemoteAddr)
				cancel()
			}
		}
	}
}
