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
	"time"
)

func loadBalancer(stream database.StreamInfo, previous *[]int) (*http.Response, string, int, error) {
	debug := os.Getenv("DEBUG") == "true"

	m3uIndexes := utils.GetM3UIndexes()

	sort.Slice(m3uIndexes, func(i, j int) bool {
		return db.ConcurrencyPriorityValue(i) > db.ConcurrencyPriorityValue(j)
	})

	maxLapsString := os.Getenv("MAX_RETRIES")
	maxLaps, err := strconv.Atoi(strings.TrimSpace(maxLapsString))
	if err != nil || maxLaps < 0 {
		maxLaps = 5
	}

	lap := 0

	for lap < maxLaps || maxLaps == 0 {
		if debug {
			log.Printf("[DEBUG] Stream attempt %d out of %d\n", lap+1, maxLaps)
		}
		allSkipped := true // Assume all URLs might be skipped

		for _, index := range m3uIndexes {
			if slices.Contains(*previous, index) {
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
				if debug {
					log.Printf("[DEBUG] Successfully fetched stream from %s\n", url)
				}
				return resp, url, index, nil
			}
			log.Printf("Error fetching stream: %s\n", err.Error())
			if debug {
				log.Printf("[DEBUG] Error fetching stream from %s: %s\n", url, err.Error())
			}

			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}

		if allSkipped {
			if debug {
				log.Printf("[DEBUG] All streams skipped in lap %d\n", lap)
			}
			*previous = []int{}
		}

		lap++
	}

	return nil, "", -1, fmt.Errorf("Error fetching stream. Exhausted all streams.")
}

func proxyStream(ctx context.Context, m3uIndex int, resp *http.Response, r *http.Request, w http.ResponseWriter, statusChan chan int) {
	debug := os.Getenv("DEBUG") == "true"

	db.UpdateConcurrency(m3uIndex, true)
	defer db.UpdateConcurrency(m3uIndex, false)

	bufferMbInt, err := strconv.Atoi(os.Getenv("BUFFER_MB"))
	if err != nil || bufferMbInt < 0 {
		bufferMbInt = 0
	}
	buffer := make([]byte, 1024)
	if bufferMbInt > 0 {
		buffer = make([]byte, bufferMbInt*1024*1024)
	}

	defer func() {
		buffer = nil
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}()

	timeoutSecond, err := strconv.Atoi(os.Getenv("STREAM_TIMEOUT"))
	if err != nil || timeoutSecond <= 0 {
		timeoutSecond = 3
	}

	timeoutDuration := time.Duration(timeoutSecond) * time.Second
	timer := time.NewTimer(timeoutDuration)
	defer timer.Stop()

	// Backoff settings
	initialBackoff := 200 * time.Millisecond
	maxBackoff := time.Duration(timeoutSecond-1) * time.Second
	currentBackoff := initialBackoff

	returnStatus := 0

	for {
		select {
		case <-ctx.Done(): // handle context cancellation
			log.Printf("Context canceled for stream: %s\n", r.RemoteAddr)
			statusChan <- 0
			return
		case <-timer.C:
			log.Printf("Timeout reached while trying to stream: %s\n", r.RemoteAddr)
			statusChan <- returnStatus
			return
		default:
			n, err := resp.Body.Read(buffer)
			if err != nil {
				if err == io.EOF {
					log.Printf("Stream ended (EOF reached): %s\n", r.RemoteAddr)
					if utils.IsPlaylist(resp) {
						statusChan <- 2
						return
					}

					returnStatus = 2
					log.Printf("Retrying same stream until timeout (%d seconds) is reached...\n", timeoutSecond)
					if debug {
						log.Printf("[DEBUG] Retrying same stream with backoff of %v...\n", currentBackoff)
					}

					time.Sleep(currentBackoff)
					currentBackoff *= 2
					if currentBackoff > maxBackoff {
						currentBackoff = maxBackoff
					}

					continue
				}

				log.Printf("Error reading stream: %s\n", err.Error())

				returnStatus = 1

				if debug {
					log.Printf("[DEBUG] Retrying same stream with backoff of %v...\n", currentBackoff)
				}

				time.Sleep(currentBackoff)
				currentBackoff *= 2
				if currentBackoff > maxBackoff {
					currentBackoff = maxBackoff
				}

				continue
			}

			if _, err := w.Write(buffer[:n]); err != nil {
				log.Printf("Error writing to response: %s\n", err.Error())
				statusChan <- 0
				return
			}

			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}

			// Reset the timer on each successful write and backoff
			if !timer.Stop() {
				select {
				case <-timer.C: // drain the channel to avoid blocking
				default:
				}
			}
			timer.Reset(timeoutDuration)

			// Reset the backoff duration after successful read/write
			currentBackoff = initialBackoff
		}
	}
}

func streamHandler(w http.ResponseWriter, r *http.Request, db *database.Instance) {
	debug := os.Getenv("DEBUG") == "true"

	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = w.Write([]byte(fmt.Sprintf("HTTP method %q not allowed", r.Method)))
		return
	}

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

	var selectedIndex int
	var selectedUrl string

	testedIndexes := []int{}
	firstWrite := true

	var resp *http.Response

	for {
		select {
		case <-ctx.Done():
			log.Printf("Client disconnected: %s\n", r.RemoteAddr)
			return
		default:
			resp, selectedUrl, selectedIndex, err = loadBalancer(stream, &testedIndexes)
			if err != nil {
				log.Printf("Error reloading stream for %s: %v\n", streamSlug, err)
				return
			}

			// HTTP header initialization
			if firstWrite {
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("Access-Control-Allow-Origin", "*")
				for k, v := range resp.Header {
					if strings.ToLower(k) != "content-length" {
						for _, val := range v {
							w.Header().Set(k, val)
						}
					}
				}
				if debug {
					log.Printf("[DEBUG] Headers set for response: %v\n", w.Header())
				}
				firstWrite = false
			}

			exitStatus := make(chan int)

			log.Printf("Proxying %s to %s\n", r.RemoteAddr, selectedUrl)
			go proxyStream(ctx, selectedIndex, resp, r, w, exitStatus)
			testedIndexes = append(testedIndexes, selectedIndex)

			streamExitCode := <-exitStatus
			log.Printf("Exit code %d received from %s\n", streamExitCode, selectedUrl)

			if streamExitCode == 2 && utils.IsPlaylist(resp) {
				log.Printf("Successfully proxied playlist: %s\n", r.RemoteAddr)
				cancel()
			} else if streamExitCode == 1 || streamExitCode == 2 {
				// Retry on server-side connection errors
				log.Printf("Retrying other servers...\n")
			} else {
				// Consider client-side connection errors as complete closure
				log.Printf("Client has closed the stream: %s\n", r.RemoteAddr)
				cancel()
			}

			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}
}
