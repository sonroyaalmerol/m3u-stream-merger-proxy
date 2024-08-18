package database

import (
	"context"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"
)

type Instance struct {
	Redis *redis.Client
	Ctx   context.Context
}

func InitializeDb(addr string, password string, db int) (*Instance, error) {
	var redisInstance *redis.Client

	if password == "" {
		redisInstance = redis.NewClient(&redis.Options{
			Addr: addr,
			DB:   db,
		})
	} else {
		redisInstance = redis.NewClient(&redis.Options{
			Addr:     addr,
			Password: password,
			DB:       db,
		})
	}

	if err := redisInstance.Ping(context.Background()).Err(); err != nil {
		return nil, fmt.Errorf("error connecting to Redis: %v", err)
	}

	return &Instance{Redis: redisInstance, Ctx: context.Background()}, nil
}

func (db *Instance) ClearDb() error {
	if err := db.Redis.FlushDB(db.Ctx).Err(); err != nil {
		return fmt.Errorf("error clearing Redis: %v", err)
	}

	return nil
}

func (db *Instance) SaveToDb(streams []StreamInfo) error {
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
		pipeline.HSet(db.Ctx, streamKey, streamData)

		for _, u := range s.URLs {
			streamURLKey := fmt.Sprintf("stream:%s:url:%d", s.Slug, u.M3UIndex)
			urlData := map[string]interface{}{
				"content":   u.Content,
				"m3u_index": u.M3UIndex,
			}
			pipeline.HSet(db.Ctx, streamURLKey, urlData)
		}

		// Add to the sorted set
		sortScore := calculateSortScore(s)
		pipeline.ZAdd(db.Ctx, "streams_sorted", redis.Z{
			Score:  sortScore,
			Member: streamKey,
		})
	}

	if len(streams) > 0 {
		_, err := pipeline.Exec(db.Ctx)
		if err != nil {
			return fmt.Errorf("SaveToDb error: %v", err)
		}
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

	return nil
}

func (db *Instance) DeleteStreamURL(s StreamInfo, m3uIndex int) error {
	if err := db.Redis.Del(db.Ctx, fmt.Sprintf("stream:%s:url:%d", s.Slug, m3uIndex)).Err(); err != nil {
		return fmt.Errorf("error deleting stream URL from Redis: %v", err)
	}

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
					pipe.HGetAll(db.Ctx, key)
				}
				return nil
			})
			if err != nil {
				return s, fmt.Errorf("error getting URL data from Redis: %v", err)
			}

			for _, result := range results {
				urlData := result.(*redis.MapStringStringCmd).Val()
				m3uIndex, _ := strconv.Atoi(urlData["m3u_index"])
				u := StreamURL{
					Content:  urlData["content"],
					M3UIndex: m3uIndex,
				}
				s.URLs = append(s.URLs, u)
			}
		}

		cursor = newCursor
		if cursor == 0 {
			break
		}
	}

	return s, nil
}

func (db *Instance) GetStreams() ([]StreamInfo, error) {
	keys, err := db.Redis.ZRange(db.Ctx, "streams_sorted", 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("error retrieving streams: %v", err)
	}

	// Create a slice to hold the final stream data
	var streams []StreamInfo
	streamKeys := make([]string, 0, len(keys))

	// Filter out URL keys
	for _, key := range keys {
		if !strings.Contains(key, ":url:") {
			streamKeys = append(streamKeys, key)
		}
	}

	// Use a pipeline to fetch all stream data in one go
	pipe := db.Redis.Pipeline()
	cmds := make([]*redis.MapStringStringCmd, len(streamKeys))

	for i, key := range streamKeys {
		cmds[i] = pipe.HGetAll(db.Ctx, key)
	}

	// Execute the pipeline
	_, err = pipe.Exec(db.Ctx)
	if err != nil {
		return nil, fmt.Errorf("error executing Redis pipeline: %v", err)
	}

	// Process the results
	for i, cmd := range cmds {
		streamData := cmd.Val()
		if len(streamData) == 0 {
			continue
		}

		slug := extractSlug(streamKeys[i])
		stream := StreamInfo{
			Slug:    slug,
			Title:   streamData["title"],
			TvgID:   streamData["tvg_id"],
			TvgChNo: streamData["tvg_chno"],
			LogoURL: streamData["logo_url"],
			Group:   streamData["group_name"],
		}

		// Fetch URLs (you may want to optimize URL fetching similarly)
		urlKeys, err := db.Redis.Keys(db.Ctx, fmt.Sprintf("%s:url:*", streamKeys[i])).Result()
		if err != nil {
			return nil, fmt.Errorf("error finding URLs for stream: %v", err)
		}

		for _, urlKey := range urlKeys {
			urlData, err := db.Redis.HGetAll(db.Ctx, urlKey).Result()
			if err != nil {
				return nil, fmt.Errorf("error getting URL data from Redis: %v", err)
			}

			m3uIndex, _ := strconv.Atoi(urlData["m3u_index"])
			u := StreamURL{
				Content:  urlData["content"],
				M3UIndex: m3uIndex,
			}
			stream.URLs = append(stream.URLs, u)
		}

		streams = append(streams, stream)
	}

	return streams, nil
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

func getSortingValue(s StreamInfo) string {
	key := os.Getenv("SORTING_KEY")

	switch key {
	case "tvg-id":
		return s.TvgID + s.Title
	case "tvg-chno":
		return s.TvgChNo + s.Title
	}

	return s.TvgID + s.Title
}

func calculateSortScore(s StreamInfo) float64 {
	// Add to the sorted set with tvg_id as the score
	maxLen := 20
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
