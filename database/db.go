package database

import (
	"context"
	"encoding/json"
	"fmt"
	"m3u-stream-merger/utils"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type Instance struct {
	Redis *redis.Client
	Ctx   context.Context
}

func InitializeDb() (*Instance, error) {
	ctx := context.Background()

	addr := os.Getenv("REDIS_ADDR")
	password := os.Getenv("REDIS_PASS")
	db := 0
	if i, err := strconv.Atoi(os.Getenv("REDIS_DB")); err == nil {
		db = i
	}

	var redisOptions *redis.Options

	if password == "" {
		redisOptions = &redis.Options{
			Addr:         addr,
			DB:           db,
			DialTimeout:  10 * time.Second,
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
		}
	} else {
		redisOptions = &redis.Options{
			Addr:         addr,
			Password:     password,
			DB:           db,
			DialTimeout:  10 * time.Second,
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
		}
	}

	redisInstance := redis.NewClient(redisOptions)

	for {
		err := redisInstance.Ping(ctx).Err()
		if err == nil {
			break
		}
		fmt.Printf("Error connecting to Redis: %v. Retrying...\n", err)
		time.Sleep(2 * time.Second)
	}

	return &Instance{Redis: redisInstance, Ctx: ctx}, nil
}

func (db *Instance) ClearDb() error {
	if err := db.Redis.FlushDB(db.Ctx).Err(); err != nil {
		return fmt.Errorf("error clearing Redis: %v", err)
	}

	return nil
}

func (db *Instance) SaveToDb(streams []*StreamInfo) error {
	var debug = os.Getenv("DEBUG") == "true"

	pipeline := db.Redis.Pipeline()

	pipeline.FlushDB(db.Ctx)

	for _, s := range streams {
		streamKey := fmt.Sprintf("stream:%s", s.Slug)

		streamDataJson, err := json.Marshal(s)
		if err != nil {
			return fmt.Errorf("SaveToDb error: %v", err)
		}

		if debug {
			utils.SafeLog("[DEBUG] Preparing to set data for stream key %s: %v\n", streamKey, s)
		}

		pipeline.Set(db.Ctx, streamKey, string(streamDataJson), 0)

		// Add to the sorted set
		sortScore := calculateSortScore(*s)

		if debug {
			utils.SafeLog("[DEBUG] Adding to sorted set with score %f and member %s\n", sortScore, streamKey)
		}

		pipeline.ZAdd(db.Ctx, "streams_sorted", redis.Z{
			Score:  sortScore,
			Member: streamKey,
		})
	}

	if len(streams) > 0 {
		if debug {
			utils.SafeLogln("[DEBUG] Executing pipeline...")
		}

		_, err := pipeline.Exec(db.Ctx)
		if err != nil {
			return fmt.Errorf("SaveToDb error: %v", err)
		}

		if debug {
			utils.SafeLogln("[DEBUG] Pipeline executed successfully.")
		}
	}

	return nil
}
func (db *Instance) DeleteStreamBySlug(slug string) error {
	streamKey := fmt.Sprintf("stream:%s", slug)

	// Delete from the sorted set
	if err := db.Redis.ZRem(db.Ctx, "streams_sorted", streamKey).Err(); err != nil {
		return fmt.Errorf("error removing stream from sorted set: %v", err)
	}

	// Delete the stream itself
	if err := db.Redis.Del(db.Ctx, streamKey).Err(); err != nil {
		return fmt.Errorf("error deleting stream from Redis: %v", err)
	}

	return nil
}

func (db *Instance) GetStreamBySlug(slug string) (StreamInfo, error) {
	streamKey := fmt.Sprintf("stream:%s", slug)
	streamDataJson, err := db.Redis.Get(db.Ctx, streamKey).Result()
	if err != nil {
		return StreamInfo{}, fmt.Errorf("error getting stream from Redis: %v", err)
	}

	stream := StreamInfo{}

	err = json.Unmarshal([]byte(streamDataJson), &stream)
	if err != nil {
		return StreamInfo{}, fmt.Errorf("error getting stream: %v", err)
	}

	if strings.TrimSpace(stream.Title) == "" {
		return StreamInfo{}, fmt.Errorf("stream not found: %s", slug)
	}

	return stream, nil
}

func (db *Instance) GetStreams() <-chan StreamInfo {
	var debug = os.Getenv("DEBUG") == "true"

	// Channels for streaming results and errors
	streamChan := make(chan StreamInfo)

	go func() {
		defer close(streamChan)

		keys, err := db.Redis.ZRange(db.Ctx, "streams_sorted", 0, -1).Result()
		if err != nil {
			utils.SafeLog("error retrieving streams: %v", err)
			return
		}

		// Filter out URL keys
		for _, key := range keys {
			streamDataJson, err := db.Redis.Get(db.Ctx, key).Result()
			if err != nil {
				utils.SafeLog("error retrieving stream data: %v", err)
				return
			}

			stream := StreamInfo{}

			err = json.Unmarshal([]byte(streamDataJson), &stream)
			if err != nil {
				utils.SafeLog("error retrieving stream data: %v", err)
				return
			}

			if debug {
				utils.SafeLog("[DEBUG] Processing stream: %v\n", stream)
			}

			// Send the stream to the channel
			streamChan <- stream
		}

		if debug {
			utils.SafeLogln("[DEBUG] Streams retrieved successfully.")
		}
	}()

	return streamChan
}

// GetConcurrency retrieves the concurrency count for the given m3uIndex
func (db *Instance) GetConcurrency(m3uIndex int) (int, error) {
	key := "concurrency:" + strconv.Itoa(m3uIndex)
	countStr, err := db.Redis.Get(db.Ctx, key).Result()
	if err == redis.Nil {
		return 0, nil // Key does not exist
	} else if err != nil {
		return 0, err
	}

	count, err := strconv.Atoi(countStr)
	if err != nil {
		return 0, err
	}

	return count, nil
}

// IncrementConcurrency increments the concurrency count for the given m3uIndex
func (db *Instance) IncrementConcurrency(m3uIndex int) error {
	key := "concurrency:" + strconv.Itoa(m3uIndex)
	return db.Redis.Incr(db.Ctx, key).Err()
}

// DecrementConcurrency decrements the concurrency count for the given m3uIndex
func (db *Instance) DecrementConcurrency(m3uIndex int) error {
	key := "concurrency:" + strconv.Itoa(m3uIndex)
	return db.Redis.Decr(db.Ctx, key).Err()
}

func (db *Instance) ClearConcurrencies() error {
	var cursor uint64
	var err error
	var keys []string

	for {
		var scanKeys []string
		scanKeys, cursor, err = db.Redis.Scan(db.Ctx, cursor, "concurrency:*", 0).Result()
		if err != nil {
			return fmt.Errorf("error scanning keys from Redis: %v", err)
		}
		keys = append(keys, scanKeys...)
		if cursor == 0 {
			break
		}
	}

	if len(keys) == 0 {
		return nil
	}

	if err := db.Redis.Del(db.Ctx, keys...).Err(); err != nil {
		return fmt.Errorf("error deleting keys from Redis: %v", err)
	}

	return nil
}

func getSortingValue(s StreamInfo) string {
	key := os.Getenv("SORTING_KEY")

	var value string
	switch key {
	case "tvg-id":
		value = s.TvgID
	case "tvg-chno":
		value = s.TvgChNo
	default:
		value = s.TvgID
	}

	// Try to parse the value as a float.
	if numValue, err := strconv.ParseFloat(value, 64); err == nil {
		return fmt.Sprintf("%010.2f", numValue) + s.Title
	}

	// If parsing fails, fall back to using the original string value.
	return value + s.Title
}

func calculateSortScore(s StreamInfo) float64 {
	// Add to the sorted set with tvg_id as the score
	maxLen := 40
	base := float64(256)

	// Normalize length by padding the string
	paddedString := strings.ToLower(getSortingValue(s))
	if len(paddedString) < maxLen {
		paddedString = paddedString + strings.Repeat("\x00", maxLen-len(paddedString))
	}

	sortScore := 0.0
	for i := 0; i < len(paddedString); i++ {
		charValue := float64(paddedString[i])
		sortScore += charValue / math.Pow(base, float64(i+1))
	}

	return sortScore
}
