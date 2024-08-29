package proxy

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"m3u-stream-merger/database"
	"m3u-stream-merger/utils"
	"net/http"
	"net/url"
	"os"
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
		log.Fatalf("Error initializing Redis database: %v", err)
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
			utils.SafeLogPrintf(nil, nil, "[DEBUG] Stream attempt %d out of %d\n", lap+1, maxLaps)
		}
		allSkipped := true // Assume all URLs might be skipped

		for _, index := range m3uIndexes {
			if slices.Contains(*previous, index) {
				utils.SafeLogPrintf(nil, nil, "Skipping M3U_%d: marked as previous stream\n", index+1)
				continue
			}

			url, ok := instance.Info.URLs[index]
			if !ok {
				utils.SafeLogPrintf(nil, nil, "Channel not found from M3U_%d: %s\n", index+1, instance.Info.Title)
				continue
			}

			if instance.Database.CheckConcurrency(index) {
				utils.SafeLogPrintf(nil, &url, "Concurrency limit reached for M3U_%d: %s\n", index+1, url)
				continue
			}

			allSkipped = false // At least one URL is not skipped

			resp, err := utils.CustomHttpRequest(method, url)
			if err == nil {
				if debug {
					utils.SafeLogPrintf(nil, &url, "[DEBUG] Successfully fetched stream from %s\n", url)
				}
				return resp, url, index, nil
			}
			utils.SafeLogPrintf(nil, &url, "Error fetching stream: %s\n", err.Error())
			if debug {
				utils.SafeLogPrintf(nil, &url, "[DEBUG] Error fetching stream from %s: %s\n", url, err.Error())
			}

			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}

		if allSkipped {
			if debug {
				utils.SafeLogPrintf(nil, nil, "[DEBUG] All streams skipped in lap %d\n", lap)
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
		content, err := io.ReadAll(resp.Body)
		if err != nil {
			log.Printf("Error reading M3U8 stream content: %v", err)
			return
		}

		if utils.EOFIsExpected(resp) {
			if !bytes.HasPrefix(content, []byte("#EXTM3U\n")) {
				log.Println("Invalid M3U8 detected. Returning response as is.")
				return
			}
			base, err := url.Parse(resp.Request.URL.String())
			if err != nil {
				log.Printf("Invalid base URL for M3U8 stream: %v", err)
				return
			}

			var output bytes.Buffer
			scanner := bufio.NewScanner(bytes.NewReader(content))

			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "#") {
					output.WriteString(line + "\n")
				} else if strings.TrimSpace(line) != "" {
					u, err := url.Parse(line)
					if err != nil {
						log.Printf("Failed to parse M3U8 URL in line: %v", err)
						output.WriteString(line + "\n")
						continue
					}

					if !u.IsAbs() {
						u = base.ResolveReference(u)
					}

					output.WriteString(u.String() + "\n")
				}
			}

			content = output.Bytes()
		}

		_, err = io.Copy(w, bytes.NewBuffer(content))
		if err != nil {
			log.Printf("Failed to write tempBuffer to response: %v", err)
			return
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
			utils.SafeLogPrintf(r, nil, "Context canceled for stream: %s\n", r.RemoteAddr)
			statusChan <- 0
			return
		case <-timer.C:
			utils.SafeLogPrintf(r, nil, "Timeout reached while trying to stream: %s\n", r.RemoteAddr)
			statusChan <- returnStatus
			return
		default:
			n, err := resp.Body.Read(buffer)
			if err != nil {
				if err == io.EOF {
					utils.SafeLogPrintf(r, nil, "Stream ended (EOF reached): %s\n", r.RemoteAddr)
					if utils.EOFIsExpected(resp) || timeoutSecond == 0 {
						statusChan <- 2
						return
					}

					returnStatus = 2
					utils.SafeLogPrintf(nil, nil, "Retrying same stream until timeout (%d seconds) is reached...\n", timeoutSecond)
					if debug {
						utils.SafeLogPrintf(nil, nil, "[DEBUG] Retrying same stream with backoff of %v...\n", currentBackoff)
					}

					time.Sleep(currentBackoff)
					currentBackoff *= 2
					if currentBackoff > maxBackoff {
						currentBackoff = maxBackoff
					}

					continue
				}

				utils.SafeLogPrintf(r, nil, "Error reading stream: %s\n", err.Error())

				returnStatus = 1

				if timeoutSecond == 0 {
					statusChan <- 1
					return
				}

				if debug {
					utils.SafeLogPrintf(nil, nil, "[DEBUG] Retrying same stream with backoff of %v...\n", currentBackoff)
				}

				time.Sleep(currentBackoff)
				currentBackoff *= 2
				if currentBackoff > maxBackoff {
					currentBackoff = maxBackoff
				}

				continue
			}

			if _, err := w.Write(buffer[:n]); err != nil {
				utils.SafeLogPrintf(r, nil, "Error writing to response: %s\n", err.Error())
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

	utils.SafeLogPrintf(r, nil, "Received request from %s for URL: %s\n", r.RemoteAddr, r.URL.Path)

	streamUrl := strings.Split(strings.TrimPrefix(r.URL.Path, "/stream/"), ".")[0]
	if streamUrl == "" {
		utils.SafeLogPrintf(r, nil, "Invalid m3uID for request from %s: %s\n", r.RemoteAddr, r.URL.Path)
		http.NotFound(w, r)
		return
	}

	stream, err := InitializeStream(strings.TrimPrefix(streamUrl, "/"))
	if err != nil {
		utils.SafeLogPrintf(r, nil, "Error retrieving stream for slug %s: %v\n", streamUrl, err)
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
			utils.SafeLogPrintf(r, nil, "Client disconnected: %s\n", r.RemoteAddr)
			return
		default:
			resp, selectedUrl, selectedIndex, err = stream.LoadBalancer(&testedIndexes, r.Method)
			if err != nil {
				utils.SafeLogPrintf(r, nil, "Error reloading stream for %s: %v\n", streamUrl, err)
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
					utils.SafeLogPrintf(r, nil, "[DEBUG] Headers set for response: %v\n", w.Header())
				}
				firstWrite = false
			}

			exitStatus := make(chan int)

			utils.SafeLogPrintf(r, &selectedUrl, "Proxying %s to %s\n", r.RemoteAddr, selectedUrl)
			go stream.ProxyStream(ctx, selectedIndex, resp, r, w, exitStatus)
			testedIndexes = append(testedIndexes, selectedIndex)

			streamExitCode := <-exitStatus
			utils.SafeLogPrintf(r, &selectedUrl, "Exit code %d received from %s\n", streamExitCode, selectedUrl)

			if streamExitCode == 2 && utils.EOFIsExpected(resp) {
				utils.SafeLogPrintf(r, nil, "Successfully proxied playlist: %s\n", r.RemoteAddr)
				cancel()
			} else if streamExitCode == 1 || streamExitCode == 2 {
				// Retry on server-side connection errors
				utils.SafeLogPrintf(r, nil, "Retrying other servers...\n")
			} else if streamExitCode == 4 {
				utils.SafeLogPrintf(r, nil, "Finished handling %s request: %s\n", r.Method, r.RemoteAddr)
				cancel()
			} else {
				// Consider client-side connection errors as complete closure
				utils.SafeLogPrintf(r, nil, "Client has closed the stream: %s\n", r.RemoteAddr)
				cancel()
			}

			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}
}
