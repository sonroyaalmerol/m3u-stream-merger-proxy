package proxy

import (
	"bufio"
	"context"
	"io"
	"m3u-stream-merger/utils"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

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

	instance.Cm.UpdateConcurrency(m3uIndex, true)
	defer instance.Cm.UpdateConcurrency(m3uIndex, false)

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
