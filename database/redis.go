package database

import "github.com/redis/go-redis/v9"

func InitializeRedis() *redis.Client {
	// Initialize Redis client
	redisClient := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379", // Change this to your Redis server address
		Password: "",               // No password set
		DB:       0,                // Use default DB
	})

	return redisClient
}
