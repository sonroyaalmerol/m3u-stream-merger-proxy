package proxy

import (
	"bufio"
	"context"
	"io"
	"m3u-stream-merger/utils"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"fmt"
)

func (instance *StreamInstance) ProxyStream(ctx context.Context, m3uIndex string, subIndex string, resp *http.Response, r *http.Request, w http.ResponseWriter, statusChan chan int) {
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
			statusChan <- returnStatus
			return
		}

		select {
		case <-ctx.Done():
			utils.SafeLogf("Context canceled for stream: %s\n", r.RemoteAddr)
			_ = resp.Body.Close()
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
				contextSleep(ctx)
			case result.err != nil:
				lastErr = time.Now()
				utils.SafeLogf("Error reading stream: %s\n", result.err.Error())
				returnStatus = 1
				if timeoutSecond == 0 {
					statusChan <- 1
					return
				}

				utils.SafeLogf("Retrying same stream until timeout (%d seconds) is reached...\n", timeoutSecond)
				contextSleep(ctx)
			case result.err == nil:

				// see if we're going to use ffmpeg
				_use_ffmpeg := os.Getenv("USE_FFMPEG") == "true"
				// get the input and output args
				_ffm_input := os.Getenv("FFMPEG_IN_ARGS") == ""
				_ffm_output := os.Getenv("FFMPEG_OUT_ARGS") == ""

				// if we are going to use ffmpeg
				if( _use_ffmpeg ) {

					// setup the command to run
					_cmd_args := fmt.Sprintf(
						"%s -i pipe:0 %s -f mpegts pipe:1",
						_ffm_input,
						_ffm_output )

					// execute the command and return the pipe
					_cmd := exec.Command( "/usr/local/bin/ffmpeg", strings.Fields( _cmd_args )[0:]...	)

					// Create a pipe for FFmpeg's stdin
					stdin, err := _cmd.StdinPipe()
					if err != nil {
						utils.SafeLogf("Failed to create stdin pipe for FFmpeg: %v", err)
						statusChan <- 0
						return
					}

					// Get the output pipe for FFmpeg
					stdout, err := _cmd.StdoutPipe()
					if err != nil {
						utils.SafeLogf("Failed to get stdout pipe for FFmpeg: %v", err)
						statusChan <- 0
						return
					}

					// Start the FFmpeg process
					if err := _cmd.Start(); err != nil {
						utils.SafeLogf("Failed to start FFmpeg process: %v", err)
						statusChan <- 0
						return
					}

					// Write the byte array to FFmpeg's stdin in a goroutine
					go func() {
						defer stdin.Close()
						if _, err := stdin.Write(buffer[:result.n]); err != nil {
							utils.SafeLogf("Error writing to FFmpeg stdin: %v", err)
						}
					}()

					// Copy FFmpeg's stdout to the HTTP response
					if _, err := io.Copy(w, stdout); err != nil {
						utils.SafeLogf("Error while copying FFmpeg output to response: %v", err)
						statusChan <- 0
						return
					}

					// Wait for the FFmpeg process to complete
					_cmd.Wait()



				// otherwise we are just using the default proxy
				} else {

					// output the bufferred/proxied stream
					if _, err := w.Write(buffer[:result.n]); err != nil {
						utils.SafeLogf("Error writing to response: %s\n", err.Error())
						statusChan <- 0
						return
					}

				}

				// flush out the buffer
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
