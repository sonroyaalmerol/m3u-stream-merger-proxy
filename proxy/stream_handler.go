package proxy

import (
	"bufio"
	"context"
	"m3u-stream-merger/database"
	"m3u-stream-merger/utils"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
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

	buffer, err := NewBuffer(initDb, streamUrl)
	if err != nil {
		utils.SafeLogf("Error initializing stream buffer: %v", err)
		return nil, err
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

func (instance *StreamInstance) StreamBuffer(ctx context.Context, w http.ResponseWriter) {
	debug := os.Getenv("DEBUG") == "true"

	streamCh, err := instance.Buffer.Subscribe(ctx)
	if err != nil {
		utils.SafeLogf("Error subscribing client: %v", err)
		return
	}
	err = instance.Database.IncrementBufferUser(instance.Buffer.streamKey)
	if err != nil && debug {
		utils.SafeLogf("Error incrementing buffer user: %v\n", err)
	}
	defer func() {
		err = instance.Database.DecrementBufferUser(instance.Buffer.streamKey)
		if err != nil && debug {
			utils.SafeLogf("Error decrementing buffer user: %v\n", err)
		}
	}()

	for {
		select {
		case <-ctx.Done(): // handle context cancellation
			return
		case chunk := <-*streamCh:
			_, err := w.Write(chunk)
			if err != nil {
				utils.SafeLogf("Error writing to client: %v", err)
				return
			}

			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
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
				go stream.StreamBuffer(ctx, w)
				go BufferStream(stream, selectedIndex, resp, r, w, exitStatus)
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

			resp.Body.Close()
		}
	}
}
