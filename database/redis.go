package database

import (
	"sync"

	"github.com/redis/go-redis/v9"
)

func InitializeRedis() *redis.Client {
	var redisClient *redis.Client
	var redisOnce sync.Once

	// Initialize Redis client
	redisOnce.Do(func() {
		redisClient = redis.NewClient(&redis.Options{
			Addr:     "localhost:6379", // Change this to your Redis server address
			Password: "",               // No password set
			DB:       0,                // Use default DB
		})
	})

	return redisClient
}
