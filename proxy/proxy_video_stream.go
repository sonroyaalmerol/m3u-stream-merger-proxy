package proxy

import (
	"context"
	"io"
	"m3u-stream-merger/utils"
	"net/http"
	"os"
	"strconv"
	"time"
)

func ProxyVideoStream(ctx context.Context, resp *http.Response, r *http.Request, w http.ResponseWriter) int {
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
	}()

	timeoutSecond := 3
	if ts, err := strconv.Atoi(os.Getenv("STREAM_TIMEOUT")); err == nil && ts >= 0 {
		timeoutSecond = ts
	}

	timeoutDuration := time.Duration(timeoutSecond) * time.Second
	if timeoutSecond == 0 {
		timeoutDuration = time.Minute
	}

	timeStarted := time.Now()
	lastErr := timeStarted

	returnStatus := 0

	// Backoff settings
	initialBackoff := 200 * time.Millisecond
	maxBackoff := time.Duration(timeoutSecond-1) * time.Second
	currentBackoff := initialBackoff

	contextSleep := func(ctx context.Context) {
		select {
		case <-ctx.Done():
			return
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

		elapsed := time.Since(timeStarted)
		if timeoutSecond > 0 && elapsed >= timeoutDuration {
			utils.SafeLogf("Timeout reached while trying to stream: %s\n", r.RemoteAddr)
			return returnStatus
		}

		select {
		case <-ctx.Done():
			utils.SafeLogf("Context canceled for stream: %s\n", r.RemoteAddr)
			_ = resp.Body.Close()
			return 0
		case result := <-readChan:
			switch {
			case result.err == io.EOF:
				lastErr = time.Now()
				if utils.EOFIsExpected(resp) || timeoutSecond == 0 {
					utils.SafeLogf("Stream ended (expected EOF reached): %s\n", r.RemoteAddr)
					return 2
				}

				utils.SafeLogf("Stream ended (unexpected EOF reached): %s\n", r.RemoteAddr)
				returnStatus = 2

				utils.SafeLogf("Retrying same stream until timeout (%d seconds) is reached...\n", timeoutSecond)
				contextSleep(ctx)
			case result.err != nil:
				lastErr = time.Now()
				utils.SafeLogf("Error reading stream: %s\n", result.err.Error())
				returnStatus = 1
				if timeoutSecond == 0 {
					return 1
				}

				utils.SafeLogf("Retrying same stream until timeout (%d seconds) is reached...\n", timeoutSecond)
				contextSleep(ctx)
			case result.err == nil:
				if _, err := w.Write(buffer[:result.n]); err != nil {
					utils.SafeLogf("Error writing to response: %s\n", err.Error())
					return 0
				}

				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}

				// check if never errored or last error was at least a second ago
				if lastErr.Equal(timeStarted) || time.Since(lastErr) >= time.Second {
					// Reset timer on successful read/write
					timeStarted = time.Now()

					currentBackoff = initialBackoff
				}
			}
		}
	}
}
