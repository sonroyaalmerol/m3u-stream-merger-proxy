package proxy

import (
	"bufio"
	"context"
	"io"
	"m3u-stream-merger/database"
	"m3u-stream-merger/utils"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

type StreamInstance struct {
	Database *database.Instance
	Info     database.StreamInfo
	Buffer   *Buffer
}

func InitializeStream(ctx context.Context, streamUrl string) (*StreamInstance, error) {
	initDb, err := database.InitializeDb()
	if err != nil {
		utils.SafeLogf("Error initializing Redis database: %v", err)
		return nil, err
	}

	if globalBuffers == nil {
		globalBuffers = make(map[string]*Buffer)
	}

	buffer, ok := globalBuffers[streamUrl]
	if !ok {
		buffer = NewBuffer()
		globalBuffers[streamUrl] = buffer
	}

	stream, err := initDb.GetStreamBySlug(streamUrl)
	if err != nil {
		return nil, err
	}

	return &StreamInstance{
		Database: initDb,
		Info:     stream,
		Buffer:   buffer,
	}, nil
}

func (instance *StreamInstance) DirectProxy(ctx context.Context, resp *http.Response, w http.ResponseWriter, statusChan chan int) {
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
				utils.SafeLogf("Failed to write line to response: %v", err)
				statusChan <- 4
				return
			}
		}
	}

	statusChan <- 4
}

func (instance *StreamInstance) BufferStream(ctx context.Context, m3uIndex int, resp *http.Response, r *http.Request, w http.ResponseWriter, statusChan chan int) {
	debug := os.Getenv("DEBUG") == "true"

	instance.Database.UpdateConcurrency(m3uIndex, true)
	defer instance.Database.UpdateConcurrency(m3uIndex, false)

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

	sourceChunk := make([]byte, 1024)

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
			n, err := resp.Body.Read(sourceChunk)
			if err != nil {
				if err == io.EOF {
					utils.SafeLogf("Stream ended (EOF reached): %s\n", r.RemoteAddr)
					if timeoutSecond == 0 {
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

			instance.Buffer.Write(sourceChunk[:n])

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

func (instance *StreamInstance) StreamBuffer(ctx context.Context, w http.ResponseWriter) {
	streamCh := instance.Buffer.Subscribe(ctx)

	for {
		select {
		case <-ctx.Done(): // handle context cancellation
			return
		case chunk := <-streamCh:
			_, err := w.Write(chunk)
			if err != nil {
				utils.SafeLogf("Error writing to client: %v", err)
				return
			}

			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		default:
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

	stream, err := InitializeStream(ctx, strings.TrimPrefix(streamUrl, "/"))
	if err != nil {
		utils.SafeLogf("Error retrieving stream for slug %s: %v\n", streamUrl, err)
		http.NotFound(w, r)
		return
	}

	var selectedIndex int
	var selectedUrl string

	firstWrite := true

	var resp *http.Response

	for {
		select {
		case <-ctx.Done():
			utils.SafeLogf("Client disconnected: %s\n", r.RemoteAddr)
			return
		default:
			resp, selectedUrl, selectedIndex, err = stream.LoadBalancer(&stream.Buffer.testedIndexes, r.Method)
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

			if r.Method != http.MethodGet || utils.EOFIsExpected(resp) {
				go stream.DirectProxy(ctx, resp, w, exitStatus)
			} else {
				go func() {
					alreadyLogged := false
					for {
						select {
						case <-ctx.Done():
							exitStatus <- 0
							return
						default:
							if !stream.Buffer.ingest.TryLock() {
								if !alreadyLogged {
									utils.SafeLogf("Using shared stream buffer with other existing clients for %s\n", r.URL.Path)
									alreadyLogged = true
								}
								continue
							}

							defer stream.Buffer.ingest.Unlock()

							stream.BufferStream(ctx, selectedIndex, resp, r, w, exitStatus)
							return
						}
					}
				}()
				go stream.StreamBuffer(ctx, w)
			}

			stream.Buffer.testedIndexes = append(stream.Buffer.testedIndexes, selectedIndex)

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
