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
	"github.com/redis/go-redis/v9"
)

// Buffer struct using Redis Streams
type Buffer struct {
	streamKey     string
	lockKey       string
	db            *database.Instance
	testedIndexes []int
	latestMsgId   string
	bufferSize    int64
	lock          *redislock.Lock
}

// NewBuffer creates a new Redis-backed buffer with a unique stream key
func NewBuffer(db *database.Instance, id string) (*Buffer, error) {
	bufferSize := 1024
	bufferMbInt, err := strconv.Atoi(os.Getenv("BUFFER_MB"))
	if err == nil && bufferMbInt > 0 {
		bufferSize = bufferMbInt * 1024
	}

	return &Buffer{
		db:            db,
		streamKey:     "streambuffer:" + id,
		lockKey:       "streambuffer:" + id + ":streaming",
		testedIndexes: []int{},
		bufferSize:    int64(bufferSize),
	}, nil
}

// Write writes data to the Redis stream
func (b *Buffer) Write(ctx context.Context, data []byte) error {
	msgId, err := b.db.Redis.XAdd(ctx, &redis.XAddArgs{
		Stream: b.streamKey,
		Values: map[string]interface{}{"data": data},
		MaxLen: b.bufferSize,
	}).Result()
	if err != nil {
		return err
	}

	b.latestMsgId = msgId

	_, err = b.db.Redis.Expire(ctx, b.streamKey, time.Minute).Result()
	return err
}

// Subscribe subscribes a client to the Redis stream and manages real-time data flow
func (b *Buffer) Subscribe(ctx context.Context) (*chan []byte, error) {
	ch := make(chan []byte)

	go func() {
		defer close(ch) // Ensure the channel is closed when the goroutine exits

		lastID := "0"

		for {
			select {
			case <-ctx.Done():
				return // Exit the goroutine if the client unsubscribes
			default:
				// Use Redis Streams to read new messages from the stream, starting from lastID
				streams, err := b.db.Redis.XRead(ctx, &redis.XReadArgs{
					Streams: []string{b.streamKey, lastID},
				}).Result()

				if err != nil {
					fmt.Printf("Error reading from stream: %v\n", err)
					time.Sleep(100 * time.Millisecond)
					continue
				}

				// Process new messages and update lastID
				for _, stream := range streams {
					for _, message := range stream.Messages {
						if data, ok := message.Values["data"].(string); ok {
							ch <- []byte(data)
							lastID = message.ID // Update lastID to track client progress
						}
					}
				}
			}
		}
	}()

	return &ch, nil
}

func BufferStream(instance *StreamInstance, m3uIndex int, resp *http.Response, r *http.Request, w http.ResponseWriter, statusChan chan int) {
	debug := os.Getenv("DEBUG") == "true"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	locker := redislock.New(instance.Database.Redis)

	lock, err := locker.Obtain(ctx, instance.Buffer.streamKey, time.Minute, nil)
	if err == redislock.ErrNotObtained {
		return
	} else if err != nil {
		utils.SafeLogf("Obtaining lock error: %v\n", err)
		return
	}
	defer lock.Release(context.Background())

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
			err := lock.Refresh(ctx, time.Minute, nil)
			if err != nil {
				utils.SafeLogf("Failed to refresh lock: %s\n", err)
			}

			clients, err := instance.Database.GetBufferUser(instance.Buffer.streamKey)
			if err != nil {
				utils.SafeLogf("Failed to get number of clients: %s\n", err)
			}

			if clients <= 0 {
				cancel()
				continue
			}

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

			err = instance.Buffer.Write(ctx, sourceChunk[:n])
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
