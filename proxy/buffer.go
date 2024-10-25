package proxy

import (
	"context"
	"fmt"
	"m3u-stream-merger/database"
	"m3u-stream-merger/utils"
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

func (b *Buffer) TryLock() bool {
	if b.lock != nil {
		err := b.lock.Refresh(context.Background(), time.Minute, nil)
		if err != nil {
			utils.SafeLogf("Refreshing lock error: %v\n", err)
			b.Unlock()
		} else {
			return true
		}
	}

	locker := redislock.New(b.db.Redis)

	lock, err := locker.Obtain(context.Background(), b.lockKey, time.Minute, nil)
	if err == redislock.ErrNotObtained {
		return false
	} else if err != nil {
		utils.SafeLogf("Obtaining lock error: %v\n", err)
	}

	b.lock = lock

	return true
}

func (b *Buffer) Unlock() {
	if b.lock == nil {
		return
	}

	err := b.lock.Release(context.Background())
	if err != nil {
		utils.SafeLogf("Error unlocking: %v\n", err)
	}

	b.lock = nil
}
