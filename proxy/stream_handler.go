package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"m3u-stream-merger/database"
	"m3u-stream-merger/utils"
	"net/http"
	"net/url"
	"os"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

type StreamInstance struct {
	Database *database.Instance
	Info     database.StreamInfo
}

func InitializeStream(streamUrl string) (*StreamInstance, error) {
	initDb, err := database.InitializeDb()
	if err != nil {
		utils.SafeLogf("Error initializing Redis database: %v", err)
		return nil, err
	}

	stream, err := initDb.GetStreamBySlug(streamUrl)
	if err != nil {
		return nil, err
	}

	return &StreamInstance{
		Database: initDb,
		Info:     stream,
	}, nil
}

func (instance *StreamInstance) LoadBalancer(previous *[]int, method string) (*http.Response, string, int, error) {
	debug := os.Getenv("DEBUG") == "true"

	m3uIndexes := utils.GetM3UIndexes()

	sort.Slice(m3uIndexes, func(i, j int) bool {
		return instance.Database.ConcurrencyPriorityValue(i) > instance.Database.ConcurrencyPriorityValue(j)
	})

	maxLapsString := os.Getenv("MAX_RETRIES")
	maxLaps, err := strconv.Atoi(strings.TrimSpace(maxLapsString))
	if err != nil || maxLaps < 0 {
		maxLaps = 5
	}

	lap := 0

	for lap < maxLaps || maxLaps == 0 {
		if debug {
			utils.SafeLogf("[DEBUG] Stream attempt %d out of %d\n", lap+1, maxLaps)
		}
		allSkipped := true // Assume all URLs might be skipped

		for _, index := range m3uIndexes {
			if slices.Contains(*previous, index) {
				utils.SafeLogf("Skipping M3U_%d: marked as previous stream\n", index+1)
				continue
			}

			url, ok := instance.Info.URLs[index]
			if !ok {
				utils.SafeLogf("Channel not found from M3U_%d: %s\n", index+1, instance.Info.Title)
				continue
			}

			if instance.Database.CheckConcurrency(index) {
				utils.SafeLogf("Concurrency limit reached for M3U_%d: %s\n", index+1, url)
				continue
			}

			allSkipped = false // At least one URL is not skipped

			resp, err := utils.CustomHttpRequest(method, url)
			if err == nil {
				if debug {
					utils.SafeLogf("[DEBUG] Successfully fetched stream from %s\n", url)
				}
				return resp, url, index, nil
			}
			utils.SafeLogf("Error fetching stream: %s\n", err.Error())
			if debug {
				utils.SafeLogf("[DEBUG] Error fetching stream from %s: %s\n", url, err.Error())
			}
		}

		if allSkipped {
			if debug {
				utils.SafeLogf("[DEBUG] All streams skipped in lap %d\n", lap)
			}
			*previous = []int{}
		}

		lap++
	}

	return nil, "", -1, fmt.Errorf("Error fetching stream. Exhausted all streams.")
}

func (instance *StreamInstance) ProxyStream(ctx context.Context, m3uIndex int, resp *http.Response, r *http.Request, w http.ResponseWriter, statusChan chan int) {
	debug := os.Getenv("DEBUG") == "true"
	bufferMbInt, err := strconv.Atoi(os.Getenv("BUFFER_MB"))
	if err != nil || bufferMbInt < 0 {
		bufferMbInt = 0
	}
	buffer := make([]byte, 1024)
	if bufferMbInt > 0 {
		buffer = make([]byte, bufferMbInt*1024*1024)
	}

	if r.Method != http.MethodGet || utils.EOFIsExpected(resp) {
		scanner := bufio.NewScanner(resp.Body)
		base, err := url.Parse(resp.Request.URL.String())
		if err != nil {
			utils.SafeLogf("Invalid base URL for M3U8 stream: %v", err)
			statusChan <- 4
			return
		}

		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "#") {
				_, err := w.Write([]byte(line + "\n"))
				if err != nil {
					utils.SafeLogf("Failed to write line to response: %v", err)
					statusChan <- 4
					return
				}
			} else if strings.TrimSpace(line) != "" {
				u, err := url.Parse(line)
				if err != nil {
					utils.SafeLogf("Failed to parse M3U8 URL in line: %v", err)
					_, err := w.Write([]byte(line + "\n"))
					if err != nil {
						utils.SafeLogf("Failed to write line to response: %v", err)
						statusChan <- 4
						return
					}
					continue
				}

				if !u.IsAbs() {
					u = base.ResolveReference(u)
				}

				_, err = w.Write([]byte(u.String() + "\n"))
				if err != nil {
					utils.SafeLogf("Failed to write URL to response: %v", err)
					statusChan <- 4
					return
				}
			}
		}

		statusChan <- 4
		return
	}

	instance.Database.UpdateConcurrency(m3uIndex, true)
	defer instance.Database.UpdateConcurrency(m3uIndex, false)

	defer func() {
		buffer = nil
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}()

	timeoutSecond, err := strconv.Atoi(os.Getenv("STREAM_TIMEOUT"))
	if err != nil || timeoutSecond < 0 {
		timeoutSecond = 3
	}

	timeoutDuration := time.Duration(timeoutSecond) * time.Second
	if timeoutSecond == 0 {
		timeoutDuration = time.Minute
	}
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
			utils.SafeLogf("Context canceled for stream: %s\n", r.RemoteAddr)
			statusChan <- 0
			return
		case <-timer.C:
			utils.SafeLogf("Timeout reached while trying to stream: %s\n", r.RemoteAddr)
			statusChan <- returnStatus
			return
		default:
			n, err := resp.Body.Read(buffer)
			if err != nil {
				if err == io.EOF {
					utils.SafeLogf("Stream ended (EOF reached): %s\n", r.RemoteAddr)
					if utils.EOFIsExpected(resp) || timeoutSecond == 0 {
						statusChan <- 2
						return
					}

					returnStatus = 2
					utils.SafeLogf("Retrying same stream until timeout (%d seconds) is reached...\n", timeoutSecond)
					if debug {
						utils.SafeLogf("[DEBUG] Retrying same stream with backoff of %v...\n", currentBackoff)
					}

					time.Sleep(currentBackoff)
					currentBackoff *= 2
					if currentBackoff > maxBackoff {
						currentBackoff = maxBackoff
					}

					continue
				}

				utils.SafeLogf("Error reading stream: %s\n", err.Error())

				returnStatus = 1

				if timeoutSecond == 0 {
					statusChan <- 1
					return
				}

				if debug {
					utils.SafeLogf("[DEBUG] Retrying same stream with backoff of %v...\n", currentBackoff)
				}

				time.Sleep(currentBackoff)
				currentBackoff *= 2
				if currentBackoff > maxBackoff {
					currentBackoff = maxBackoff
				}

				continue
			}

			if _, err := w.Write(buffer[:n]); err != nil {
				utils.SafeLogf("Error writing to response: %s\n", err.Error())
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

func Handler(w http.ResponseWriter, r *http.Request) {
	debug := os.Getenv("DEBUG") == "true"

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	utils.SafeLogf("Received request from %s for URL: %s\n", r.RemoteAddr, r.URL.Path)

	streamUrl := strings.Split(path.Base(r.URL.Path), ".")[0]
	if streamUrl == "" {
		utils.SafeLogf("Invalid m3uID for request from %s: %s\n", r.RemoteAddr, r.URL.Path)
		http.NotFound(w, r)
		return
	}

	stream, err := InitializeStream(strings.TrimPrefix(streamUrl, "/"))
	if err != nil {
		utils.SafeLogf("Error retrieving stream for slug %s: %v\n", streamUrl, err)
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
			utils.SafeLogf("Client disconnected: %s\n", r.RemoteAddr)
			return
		default:
			resp, selectedUrl, selectedIndex, err = stream.LoadBalancer(&testedIndexes, r.Method)
			if err != nil {
				utils.SafeLogf("Error reloading stream for %s: %v\n", streamUrl, err)
				return
			}

			// HTTP header initialization
			if firstWrite {
				for k, v := range resp.Header {
					if strings.ToLower(k) == "content-length" {
						continue
					}

					for _, val := range v {
						w.Header().Set(k, val)
					}
				}
				w.WriteHeader(resp.StatusCode)

				if debug {
					utils.SafeLogf("[DEBUG] Headers set for response: %v\n", w.Header())
				}
				firstWrite = false
			}

			exitStatus := make(chan int)

			utils.SafeLogf("Proxying %s to %s\n", r.RemoteAddr, selectedUrl)
			go stream.ProxyStream(ctx, selectedIndex, resp, r, w, exitStatus)
			testedIndexes = append(testedIndexes, selectedIndex)

			streamExitCode := <-exitStatus
			utils.SafeLogf("Exit code %d received from %s\n", streamExitCode, selectedUrl)

			if streamExitCode == 2 && utils.EOFIsExpected(resp) {
				utils.SafeLogf("Successfully proxied playlist: %s\n", r.RemoteAddr)
				cancel()
			} else if streamExitCode == 1 || streamExitCode == 2 {
				// Retry on server-side connection errors
				utils.SafeLogf("Retrying other servers...\n")
			} else if streamExitCode == 4 {
				utils.SafeLogf("Finished handling %s request: %s\n", r.Method, r.RemoteAddr)
				cancel()
			} else {
				// Consider client-side connection errors as complete closure
				utils.SafeLogf("Client has closed the stream: %s\n", r.RemoteAddr)
				cancel()
			}

			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}
}
