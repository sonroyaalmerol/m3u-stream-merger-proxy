package database

import (
	"context"
	"fmt"
	"log"
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
	Cache *Cache
}

func InitializeDb(addr string, password string, db int) (*Instance, error) {
	var redisOptions *redis.Options

	if password == "" {
		redisOptions = &redis.Options{
			Addr:         addr,
			DB:           db,
			DialTimeout:  1 * time.Minute,
			ReadTimeout:  1 * time.Minute,
			WriteTimeout: 1 * time.Minute,
		}
	} else {
		redisOptions = &redis.Options{
			Addr:         addr,
			Password:     password,
			DB:           db,
			DialTimeout:  1 * time.Minute,
			ReadTimeout:  1 * time.Minute,
			WriteTimeout: 1 * time.Minute,
		}
	}

	redisInstance := redis.NewClient(redisOptions)

	if err := redisInstance.Ping(context.Background()).Err(); err != nil {
		return nil, fmt.Errorf("error connecting to Redis: %v", err)
	}

	return &Instance{Redis: redisInstance, Ctx: context.Background(), Cache: NewCache()}, nil
}

func (db *Instance) ClearDb() error {
	if err := db.Redis.FlushDB(db.Ctx).Err(); err != nil {
		return fmt.Errorf("error clearing Redis: %v", err)
	}

	db.Cache.Clear("streams_sorted_cache")

	return nil
}

func (db *Instance) SaveToDb(streams []StreamInfo) error {
	var debug = os.Getenv("DEBUG") == "true"

	pipeline := db.Redis.Pipeline()

	for _, s := range streams {
		streamKey := fmt.Sprintf("stream:%s", s.Slug)
		streamData := map[string]interface{}{
			"title":      s.Title,
			"tvg_id":     s.TvgID,
			"tvg_chno":   s.TvgChNo,
			"logo_url":   s.LogoURL,
			"group_name": s.Group,
		}

		if debug {
			log.Printf("[DEBUG] Preparing to set data for stream key %s: %v\n", streamKey, streamData)
		}

		pipeline.HSet(db.Ctx, streamKey, streamData)

		for index, u := range s.URLs {
			streamURLKey := fmt.Sprintf("stream:%s:url:%d", s.Slug, index)

			if debug {
				log.Printf("[DEBUG] Preparing to set URL for key %s: %s\n", streamURLKey, u)
			}

			pipeline.Set(db.Ctx, streamURLKey, u, 0)
		}

		// Add to the sorted set
		sortScore := calculateSortScore(s)

		if debug {
			log.Printf("[DEBUG] Adding to sorted set with score %f and member %s\n", sortScore, streamKey)
		}

		pipeline.ZAdd(db.Ctx, "streams_sorted", redis.Z{
			Score:  sortScore,
			Member: streamKey,
		})
	}

	if len(streams) > 0 {
		if debug {
			log.Println("[DEBUG] Executing pipeline...")
		}

		_, err := pipeline.Exec(db.Ctx)
		if err != nil {
			return fmt.Errorf("SaveToDb error: %v", err)
		}

		if debug {
			log.Println("[DEBUG] Pipeline executed successfully.")
		}
	}

	db.Cache.Clear("streams_sorted_cache")

	if debug {
		log.Println("[DEBUG] Cache cleared.")
	}

	return nil
}
func (db *Instance) DeleteStreamBySlug(slug string) error {
	streamKey := fmt.Sprintf("stream:%s", slug)

	// Delete associated URLs
	cursor := uint64(0)
	for {
		keys, newCursor, err := db.Redis.Scan(db.Ctx, cursor, fmt.Sprintf("%s:url:*", streamKey), 10).Result()
		if err != nil {
			return fmt.Errorf("error scanning associated URLs: %v", err)
		}

		for _, key := range keys {
			if err := db.Redis.Del(db.Ctx, key).Err(); err != nil {
				return fmt.Errorf("error deleting stream URL from Redis: %v", err)
			}
		}

		cursor = newCursor
		if cursor == 0 {
			break
		}
	}

	// Delete from the sorted set
	if err := db.Redis.ZRem(db.Ctx, "streams_sorted", streamKey).Err(); err != nil {
		return fmt.Errorf("error removing stream from sorted set: %v", err)
	}

	// Delete the stream itself
	if err := db.Redis.Del(db.Ctx, streamKey).Err(); err != nil {
		return fmt.Errorf("error deleting stream from Redis: %v", err)
	}

	db.Cache.Clear("streams_sorted_cache")
	return nil
}

func (db *Instance) DeleteStreamURL(s StreamInfo, m3uIndex int) error {
	if err := db.Redis.Del(db.Ctx, fmt.Sprintf("stream:%s:url:%d", s.Slug, m3uIndex)).Err(); err != nil {
		return fmt.Errorf("error deleting stream URL from Redis: %v", err)
	}

	db.Cache.Clear("streams_sorted_cache")
	return nil
}

func (db *Instance) GetStreamBySlug(slug string) (StreamInfo, error) {
	streamKey := fmt.Sprintf("stream:%s", slug)
	streamData, err := db.Redis.HGetAll(db.Ctx, streamKey).Result()
	if err != nil {
		return StreamInfo{}, fmt.Errorf("error getting stream from Redis: %v", err)
	}

	if len(streamData) == 0 {
		return StreamInfo{}, fmt.Errorf("stream not found: %s", slug)
	}

	s := StreamInfo{
		Slug:    slug,
		Title:   streamData["title"],
		TvgID:   streamData["tvg_id"],
		TvgChNo: streamData["tvg_chno"],
		LogoURL: streamData["logo_url"],
		Group:   streamData["group_name"],
		URLs:    map[int]string{},
	}

	cursor := uint64(0)
	for {
		keys, newCursor, err := db.Redis.Scan(db.Ctx, cursor, fmt.Sprintf("%s:url:*", streamKey), 10).Result()
		if err != nil {
			return s, fmt.Errorf("error finding URLs for stream: %v", err)
		}

		if len(keys) > 0 {
			results, err := db.Redis.Pipelined(db.Ctx, func(pipe redis.Pipeliner) error {
				for _, key := range keys {
					pipe.Get(db.Ctx, key)
				}
				return nil
			})
			if err != nil {
				return s, fmt.Errorf("error getting URL data from Redis: %v", err)
			}

			for i, result := range results {
				urlData := result.(*redis.StringCmd).Val()

				m3uIndex, err := strconv.Atoi(extractM3UIndex(keys[i]))
				if err != nil {
					return s, fmt.Errorf("m3u index is not an integer: %v", err)
				}
				s.URLs[m3uIndex] = urlData
			}
		}

		cursor = newCursor
		if cursor == 0 {
			break
		}
	}

	return s, nil
}

func (db *Instance) GetStreams() <-chan StreamInfo {
	var debug = os.Getenv("DEBUG") == "true"

	// Channels for streaming results and errors
	streamChan := make(chan StreamInfo)

	go func() {
		defer close(streamChan)

		// Check if the data is in the cache
		cacheKey := "streams_sorted_cache"
		if data, found := db.Cache.Get(cacheKey); found {
			if debug {
				log.Printf("[DEBUG] Cache hit for key %s\n", cacheKey)
			}
			for _, stream := range data {
				streamChan <- stream
			}
			return
		}

		if debug {
			log.Println("[DEBUG] Cache miss. Retrieving streams from Redis...")
		}

		keys, err := db.Redis.ZRange(db.Ctx, "streams_sorted", 0, -1).Result()
		if err != nil {
			log.Printf("error retrieving streams: %v", err)
			return
		}

		// Store the result in the cache
		var streams []StreamInfo

		// Filter out URL keys
		for _, key := range keys {
			if !strings.Contains(key, ":url:") {
				streamData, err := db.Redis.HGetAll(db.Ctx, key).Result()
				if err != nil {
					log.Printf("error retrieving stream data: %v", err)
					return
				}

				slug := extractSlug(key)
				stream := StreamInfo{
					Slug:    slug,
					Title:   streamData["title"],
					TvgID:   streamData["tvg_id"],
					TvgChNo: streamData["tvg_chno"],
					LogoURL: streamData["logo_url"],
					Group:   streamData["group_name"],
					URLs:    map[int]string{},
				}

				if debug {
					log.Printf("[DEBUG] Processing stream: %v\n", stream)
				}

				urlKeys, err := db.Redis.Keys(db.Ctx, fmt.Sprintf("%s:url:*", key)).Result()
				if err != nil {
					log.Printf("error finding URLs for stream: %v", err)
					return
				}

				for _, urlKey := range urlKeys {
					urlData, err := db.Redis.Get(db.Ctx, urlKey).Result()
					if err != nil {
						log.Printf("error getting URL data from Redis: %v", err)
						return
					}

					m3uIndex, err := strconv.Atoi(extractM3UIndex(urlKey))
					if err != nil {
						log.Printf("m3u index is not an integer: %v", err)
						return
					}
					stream.URLs[m3uIndex] = urlData
				}

				// Send the stream to the channel
				streams = append(streams, stream)
				streamChan <- stream
			}
		}

		db.Cache.Set(cacheKey, streams)

		if debug {
			log.Println("[DEBUG] Streams retrieved and cached successfully.")
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
	if err := db.Redis.Del(db.Ctx, "concurrency:*").Err(); err != nil {
		return fmt.Errorf("error clear concurrencies from Redis: %v", err)
	}

	return nil
}

func extractSlug(key string) string {
	parts := strings.Split(key, ":")
	if len(parts) > 1 {
		return parts[1]
	}
	return ""
}

func extractM3UIndex(key string) string {
	parts := strings.Split(key, ":")
	if len(parts) > 1 {
		return parts[3]
	}
	return ""
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
