package proxy

import (
	"bufio"
	"context"
	"fmt"
	"m3u-stream-merger/database"
	"m3u-stream-merger/utils"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
)

var streamStatusChans map[string]*chan int

type ClientInstance struct {
	Database *database.Instance
}

func InitializeClient(ctx context.Context, streamUrl string) (*ClientInstance, error) {
	initDb, err := database.InitializeDb()
	if err != nil {
		utils.SafeLogf("Error initializing Redis database: %v", err)
		return nil, err
	}

	return &ClientInstance{
		Database: initDb,
	}, nil
}

func (instance *ClientInstance) DirectProxy(ctx context.Context, resp *http.Response, w http.ResponseWriter) error {
	scanner := bufio.NewScanner(resp.Body)
	base, err := url.Parse(resp.Request.URL.String())
	if err != nil {
		return fmt.Errorf("Invalid base URL for M3U8 stream: %v", err)
	}

	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#") {
			_, err := w.Write([]byte(line + "\n"))
			if err != nil {
				return fmt.Errorf("Failed to write line to response: %v", err)
			}
		} else if strings.TrimSpace(line) != "" {
			u, err := url.Parse(line)
			if err != nil {
				utils.SafeLogf("Failed to parse M3U8 URL in line: %v", err)
				_, err := w.Write([]byte(line + "\n"))
				if err != nil {
					return fmt.Errorf("Failed to write line to response: %v", err)
				}
				continue
			}

			if !u.IsAbs() {
				u = base.ResolveReference(u)
			}

			_, err = w.Write([]byte(u.String() + "\n"))
			if err != nil {
				return fmt.Errorf("Failed to write line to response: %v", err)
			}
		}
	}

	return nil
}

func (instance *ClientInstance) StreamBuffer(ctx context.Context, buffer *Buffer, w http.ResponseWriter) error {
	debug := os.Getenv("DEBUG") == "true"

	streamCh, err := buffer.Subscribe(ctx)
	if err != nil {
		return fmt.Errorf("Error subscribing client: %v", err)
	}
	err = instance.Database.IncrementBufferUser(buffer.streamKey)
	if err != nil && debug {
		utils.SafeLogf("Error incrementing buffer user: %v\n", err)
	}
	defer func() {
		err = instance.Database.DecrementBufferUser(buffer.streamKey)
		if err != nil && debug {
			utils.SafeLogf("Error decrementing buffer user: %v\n", err)
		}
	}()

	for {
		select {
		case <-ctx.Done(): // handle context cancellation
			return nil
		case chunk := <-*streamCh:
			_, err := w.Write(chunk)
			if err != nil {
				return fmt.Errorf("Error writing to client: %v", err)
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

	if streamStatusChans == nil {
		streamStatusChans = make(map[string]*chan int)
	}

	utils.SafeLogf("Received request from %s for URL: %s\n", r.RemoteAddr, r.URL.Path)

	streamUrl := strings.Split(path.Base(r.URL.Path), ".")[0]
	if streamUrl == "" {
		utils.SafeLogf("Invalid m3uID for request from %s: %s\n", r.RemoteAddr, r.URL.Path)
		http.NotFound(w, r)
		return
	}

	streamUrl = strings.TrimPrefix(streamUrl, "/")

	client, err := InitializeClient(ctx, streamUrl)
	if err != nil {
		utils.SafeLogf("Error retrieving stream for slug %s: %v\n", streamUrl, err)
		http.NotFound(w, r)
		return
	}

	stream, err := InitializeBufferStream(streamUrl)
	if err != nil {
		utils.SafeLogf("Error retrieving buffer stream for slug %s: %v\n", streamUrl, err)
		http.NotFound(w, r)
		return
	}

	err = stream.Start(r)
	if err != nil {
		utils.SafeLogf("Error starting buffer stream for slug %s: %v\n", streamUrl, err)
		http.NotFound(w, r)
		return
	}

	utils.SafeLogf("Proxying %s to %s\n", stream.Info.Title, r.RemoteAddr)

	for k, v := range stream.Response.Header {
		if strings.ToLower(k) == "content-length" {
			continue
		}

		for _, val := range v {
			w.Header().Set(k, val)
		}
	}
	w.WriteHeader(stream.Response.StatusCode)

	if debug {
		utils.SafeLogf("[DEBUG] Headers set for response: %v\n", w.Header())
	}

	if r.Method != http.MethodGet || utils.EOFIsExpected(stream.Response) {
		err = client.DirectProxy(ctx, stream.Response, w)
	} else {
		err = client.StreamBuffer(ctx, stream.Buffer, w)
	}

	utils.SafeLogf("Error stream for slug %s: %v\n", streamUrl, err)
	http.Error(w, err.Error(), http.StatusInternalServerError)
}
