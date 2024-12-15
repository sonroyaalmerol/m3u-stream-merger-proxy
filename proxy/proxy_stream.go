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
	defer func() {
		if debug {
			utils.SafeLogf("[DEBUG] Defer executed for stream: %s\n", r.RemoteAddr)
		}
		instance.Cm.UpdateConcurrency(m3uIndex, false)
	}()

	defer func() {
		buffer = nil
	}()

	timeoutSecond := 3
	if ts, err := strconv.Atoi(os.Getenv("STREAM_TIMEOUT")); err == nil && ts >= 0 {
		timeoutSecond = ts
	}

	timeoutDuration := time.Duration(timeoutSecond) * time.Second
	if timeoutSecond == 0 {
		timeoutDuration = time.Minute
	}
	timer := time.NewTimer(timeoutDuration)
	defer timer.Stop()

	timeStarted := time.Now()
	lastErr := timeStarted

	returnStatus := 0

	// Backoff settings
	initialBackoff := 200 * time.Millisecond
	maxBackoff := time.Duration(timeoutSecond-1) * time.Second
	currentBackoff := initialBackoff

	contextSleep := func(ctx context.Context, timer *time.Timer) {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-time.After(currentBackoff):
			currentBackoff *= 2
			if currentBackoff > maxBackoff {
				currentBackoff = maxBackoff
			}
		}
	}

	readChan := make(chan struct {
		n   int
		err error
	}, 1)

	for {
		go func() {
			n, err := resp.Body.Read(buffer)
			readChan <- struct {
				n   int
				err error
			}{n, err}
		}()

		select {
		case <-ctx.Done():
			utils.SafeLogf("Context canceled for stream: %s\n", r.RemoteAddr)
			return
		case <-timer.C:
			utils.SafeLogf("Timeout reached while trying to stream: %s\n", r.RemoteAddr)
			statusChan <- returnStatus
			return
		case result := <-readChan:
			switch {
			case result.err == io.EOF:
				lastErr = time.Now()
				if utils.EOFIsExpected(resp) || timeoutSecond == 0 {
					utils.SafeLogf("Stream ended (expected EOF reached): %s\n", r.RemoteAddr)
					statusChan <- 2
					return
				}

				utils.SafeLogf("Stream ended (unexpected EOF reached): %s\n", r.RemoteAddr)
				returnStatus = 2

				utils.SafeLogf("Retrying same stream until timeout (%d seconds) is reached...\n", timeoutSecond)
				contextSleep(ctx, timer)

				continue
			case result.err != nil:
				lastErr = time.Now()
				utils.SafeLogf("Error reading stream: %s\n", err.Error())
				returnStatus = 1
				if timeoutSecond == 0 {
					statusChan <- 1
					return
				}

				utils.SafeLogf("Retrying same stream until timeout (%d seconds) is reached...\n", timeoutSecond)
				contextSleep(ctx, timer)

				continue
			}

			if _, err := w.Write(buffer[:result.n]); err != nil {
				utils.SafeLogf("Error writing to response: %s\n", err.Error())
				statusChan <- 0
				return
			}

			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}

			// check if never errored or last error was at least a second ago
			if lastErr.Equal(timeStarted) || time.Since(lastErr) >= time.Second {
				// Reset timer on successful read/write
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(timeoutDuration)

				currentBackoff = initialBackoff
			}
		}
	}
}
