package proxy

import (
	"context"
	"fmt"
	"io"
	"m3u-stream-merger/database"
	"m3u-stream-merger/utils"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/bsm/redislock"
)

var BufferStreams map[string]*BufferStream

type BufferStream struct {
	Id            string
	TestedIndexes []int
	Buffer        *Buffer
	Info          database.StreamInfo
	Database      *database.Instance
	Started       bool
	Response      *http.Response
}

func InitializeBufferStream(streamUrl string) (*BufferStream, error) {
	if BufferStreams == nil {
		BufferStreams = make(map[string]*BufferStream)
	}

	if BufferStreams[streamUrl] != nil {
		return BufferStreams[streamUrl], nil
	}

	db, err := database.InitializeDb()
	if err != nil {
		return nil, fmt.Errorf("InitializeBufferStream error: %v", err)
	}

	stream, err := db.GetStreamBySlug(streamUrl)
	if err != nil {
		return nil, fmt.Errorf("InitializeBufferStream error: %v", err)
	}

	buffer, err := NewBuffer(db, streamUrl)
	if err != nil {
		return nil, fmt.Errorf("InitializeBufferStream error: %v", err)
	}

	BufferStreams[streamUrl] = &BufferStream{
		Id:            "streambuffer:" + streamUrl,
		TestedIndexes: make([]int, 0),
		Buffer:        buffer,
		Database:      db,
		Info:          stream,
		Started:       false,
	}

	return BufferStreams[streamUrl], nil
}

func (stream *BufferStream) Start(r *http.Request) error {
	debug := os.Getenv("DEBUG") == "true"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if stream.Started {
		return nil
	}

	stream.Started = true
	defer func() {
		stream.Started = false
	}()

	resp, selectedIndex, err := stream.LoadBalancer(r.Method)
	if err != nil {
		return err
	}

	stream.Response = resp

	firstIteration := true

	go func() {
		for {
			select {
			case <-ctx.Done():
				utils.SafeLogf("Context cancelled: %s\n", stream.Info.Title)
				resp.Body.Close()
				return
			default:
				if !firstIteration {
					resp, selectedIndex, err = stream.LoadBalancer(r.Method)
					if err != nil {
						utils.SafeLogf("Error reloading stream for %s: %v\n", stream.Info.Slug, err)
						return
					}
					stream.Response = resp
				} else {
					firstIteration = false
				}

				stream.Buffer.testedIndexes = append(stream.Buffer.testedIndexes, selectedIndex)
				locker := redislock.New(stream.Database.Redis)

				lock, err := locker.Obtain(ctx, stream.Id, time.Minute, nil)
				if err == redislock.ErrNotObtained {
					return
				} else if err != nil {
					utils.SafeLogf("Obtaining lock error: %v\n", err)
					return
				}
				defer func() {
					err := lock.Release(context.Background())
					if err != nil && debug {
						utils.SafeLogf("Releasing lock error: %v\n", err)
					}
				}()

				stream.streamToBuffer(ctx, resp, lock, selectedIndex)
				resp.Body.Close()
			}
		}
	}()

	return nil
}

func (stream *BufferStream) streamToBuffer(ctx context.Context, resp *http.Response, lock *redislock.Lock, selectedIndex int) {
	debug := os.Getenv("DEBUG") == "true"

	stream.Database.UpdateConcurrency(selectedIndex, true)
	defer stream.Database.UpdateConcurrency(selectedIndex, false)

	timeoutSecond, err := strconv.Atoi(os.Getenv("STREAM_TIMEOUT"))
	if err != nil || timeoutSecond < 0 {
		timeoutSecond = 3
	}

	lastClientSeen := time.Now()

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

	sourceChunk := make([]byte, 1024)

	for {
		select {
		case <-ctx.Done(): // handle context cancellation
			utils.SafeLogf("Context canceled for stream: %s\n", stream.Info.Title)
			return
		case <-timer.C:
			utils.SafeLogf("Timeout reached while trying to stream: %s\n", stream.Info.Title)
			return
		default:
			err := lock.Refresh(ctx, time.Minute, nil)
			if err != nil {
				utils.SafeLogf("Failed to refresh lock: %s\n", err)
			}

			clients, err := stream.Database.GetBufferUser(stream.Id)
			if err != nil {
				utils.SafeLogf("Failed to get number of clients: %s\n", err)
			}

			if clients <= 0 && time.Since(lastClientSeen) > 3*time.Second {
				utils.SafeLogf("No clients left: %s\n", stream.Info.Title)
				return
			}
			lastClientSeen = time.Now()

			n, err := resp.Body.Read(sourceChunk)
			if err != nil {
				if err == io.EOF {
					utils.SafeLogf("Stream ended (EOF reached): %s\n", stream.Info.Title)
					if timeoutSecond == 0 {
						return
					}

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

				if timeoutSecond == 0 {
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

			err = stream.Buffer.Write(ctx, sourceChunk[:n])
			if err != nil {
				utils.SafeLogf("Failed to store buffer: %s\n", err.Error())
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
