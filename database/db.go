package database

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
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
	for _, s := range streams {
		streamKey := fmt.Sprintf("stream:%s", s.Title)
		streamData := map[string]interface{}{
			"id":         s.DbId,
			"title":      s.Title,
			"tvg_id":     s.TvgID,
			"logo_url":   s.LogoURL,
			"group_name": s.Group,
		}

		if err := db.Redis.HSet(db.Ctx, streamKey, streamData).Err(); err != nil {
			return fmt.Errorf("error saving stream to Redis: %v", err)
		}

		// Add to the sorted set with tvg_id as the score
		h := fnv.New64a()
		h.Write([]byte(s.TvgID))
		hash := h.Sum64()
		tvgIDScore := float64(hash) / math.MaxUint64

		if err := db.Redis.ZAdd(db.Ctx, "streams_sorted_by_tvg_id", redis.Z{
			Score:  tvgIDScore,
			Member: streamKey,
		}).Err(); err != nil {
			return fmt.Errorf("error adding stream to sorted set: %v", err)
		}

		for _, u := range s.URLs {
			urlKey := fmt.Sprintf("%s:url:%d", streamKey, u.M3UIndex)
			urlData := map[string]interface{}{
				"content":   u.Content,
				"m3u_index": u.M3UIndex,
			}

			if err := db.Redis.HSet(db.Ctx, urlKey, urlData).Err(); err != nil {
				return fmt.Errorf("error saving stream URL to Redis: %v", err)
			}
		}
	}

	return nil
}

func (db *Instance) InsertStream(s StreamInfo) (int64, error) {
	streamKey := fmt.Sprintf("stream:%s", s.Title)
	streamData := map[string]interface{}{
		"title":      s.Title,
		"tvg_id":     s.TvgID,
		"logo_url":   s.LogoURL,
		"group_name": s.Group,
	}

	if err := db.Redis.HSet(db.Ctx, streamKey, streamData).Err(); err != nil {
		return -1, fmt.Errorf("error inserting stream to Redis: %v", err)
	}

	// Add to the sorted set with tvg_id as the score
	h := fnv.New64a()
	h.Write([]byte(s.TvgID))
	hash := h.Sum64()
	tvgIDScore := float64(hash) / math.MaxUint64

	if err := db.Redis.ZAdd(db.Ctx, "streams_sorted_by_tvg_id", redis.Z{
		Score:  tvgIDScore,
		Member: streamKey,
	}).Err(); err != nil {
		return -1, fmt.Errorf("error adding stream to sorted set: %v", err)
	}

	return s.DbId, nil
}

func (db *Instance) InsertStreamUrl(id int64, url StreamURL) (int64, error) {
	streamKey := fmt.Sprintf("stream:%d:url:%d", id, url.M3UIndex)
	urlData := map[string]interface{}{
		"content":   url.Content,
		"m3u_index": url.M3UIndex,
	}

	if err := db.Redis.HSet(db.Ctx, streamKey, urlData).Err(); err != nil {
		return -1, fmt.Errorf("error inserting stream URL to Redis: %v", err)
	}

	return id, nil
}

func (db *Instance) DeleteStreamByTitle(title string) error {
	streamKey := fmt.Sprintf("stream:%s", title)

	// Delete associated URLs
	keys, err := db.Redis.Keys(db.Ctx, fmt.Sprintf("%s:url:*", streamKey)).Result()
	if err != nil {
		return fmt.Errorf("error finding associated URLs: %v", err)
	}
	for _, key := range keys {
		if err := db.Redis.Del(db.Ctx, key).Err(); err != nil {
			return fmt.Errorf("error deleting stream URL from Redis: %v", err)
		}
	}

	// Delete from the sorted set
	if err := db.Redis.ZRem(db.Ctx, "streams_sorted_by_tvg_id", streamKey).Err(); err != nil {
		return fmt.Errorf("error removing stream from sorted set: %v", err)
	}

	// Delete the stream itself
	if err := db.Redis.Del(db.Ctx, streamKey).Err(); err != nil {
		return fmt.Errorf("error deleting stream from Redis: %v", err)
	}

	return nil
}

func (db *Instance) DeleteStreamURL(streamURLID int64) error {
	// Assuming `streamURLID` is actually a composite key, e.g., "stream:<id>:url:<m3u_index>"
	if err := db.Redis.Del(db.Ctx, fmt.Sprintf("stream:url:%d", streamURLID)).Err(); err != nil {
		return fmt.Errorf("error deleting stream URL from Redis: %v", err)
	}

	return nil
}

func (db *Instance) GetStreamByTitle(title string) (StreamInfo, error) {
	streamKey := fmt.Sprintf("stream:%s", title)
	streamData, err := db.Redis.HGetAll(db.Ctx, streamKey).Result()
	if err != nil {
		return StreamInfo{}, fmt.Errorf("error getting stream from Redis: %v", err)
	}

	if len(streamData) == 0 {
		return StreamInfo{}, fmt.Errorf("stream not found: %s", title)
	}

	id, _ := strconv.ParseInt(streamData["id"], 10, 64)

	s := StreamInfo{
		DbId:    id,
		Title:   streamData["title"],
		TvgID:   streamData["tvg_id"],
		LogoURL: streamData["logo_url"],
		Group:   streamData["group_name"],
	}

	// Fetch URLs
	keys, err := db.Redis.Keys(db.Ctx, fmt.Sprintf("%s:url:*", streamKey)).Result()
	if err != nil {
		return s, fmt.Errorf("error finding URLs for stream: %v", err)
	}

	for _, key := range keys {
		urlData, err := db.Redis.HGetAll(db.Ctx, key).Result()
		if err != nil {
			return s, fmt.Errorf("error getting URL data from Redis: %v", err)
		}

		m3uIndex, _ := strconv.Atoi(urlData["m3u_index"])
		u := StreamURL{
			Content:  urlData["content"],
			M3UIndex: m3uIndex,
		}
		s.URLs = append(s.URLs, u)
	}

	return s, nil
}

func (db *Instance) GetStreamUrlByUrlAndIndex(url string, m3u_index int) (StreamURL, error) {
	keys, err := db.Redis.Keys(db.Ctx, fmt.Sprintf("stream:*:url:%d", m3u_index)).Result()
	if err != nil {
		return StreamURL{}, fmt.Errorf("error finding URL by index: %v", err)
	}

	for _, key := range keys {
		urlData, err := db.Redis.HGetAll(db.Ctx, key).Result()
		if err != nil {
			return StreamURL{}, fmt.Errorf("error getting URL data from Redis: %v", err)
		}

		if urlData["content"] == url {
			m3uIndex, _ := strconv.Atoi(urlData["m3u_index"])
			return StreamURL{
				DbId:     extractStreamID(key),
				Content:  urlData["content"],
				M3UIndex: m3uIndex,
			}, nil
		}
	}

	return StreamURL{}, fmt.Errorf("stream URL not found: %s, index: %d", url, m3u_index)
}

func (db *Instance) GetStreams() ([]StreamInfo, error) {
	keys, err := db.Redis.ZRange(db.Ctx, "streams_sorted_by_tvg_id", 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("error retrieving streams: %v", err)
	}

	var streams []StreamInfo
	for _, key := range keys {
		if !strings.Contains(key, ":url:") { // Exclude URL keys
			s, err := db.GetStreamByTitle(extractTitle(key))
			if err != nil {
				return nil, err
			}
			streams = append(streams, s)
		}
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

// Helper functions to extract parts of the keys
func extractStreamID(key string) int64 {
	parts := strings.Split(key, ":")
	if len(parts) > 1 {
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		return id
	}
	return -1
}

func extractTitle(key string) string {
	parts := strings.Split(key, ":")
	if len(parts) > 1 {
		return parts[1]
	}
	return ""
}
