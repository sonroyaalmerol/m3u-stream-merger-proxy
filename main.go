package main

import (
	"context"
	"fmt"
	"log"
	"m3u-stream-merger/database"
	"m3u-stream-merger/m3u"
	"net/http"
	"os"
	"strconv"
	"time"
)

func updateSource(ctx context.Context, m3uUrl string, index int, maxConcurrency int) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			fmt.Printf("Background process: Updating M3U #%d from %s\n", index, m3uUrl)
			err := m3u.ParseM3UFromURL(m3uUrl, index, maxConcurrency)
			if err != nil {
				fmt.Printf("Error updating M3U: %v\n", err)
			} else {
				fmt.Printf("Background process: Updated M3U #%d from %s\n", index, m3uUrl)
			}

			updateIntervalInHour, exists := os.LookupEnv("UPDATE_INTERVAL")
			if !exists {
				updateIntervalInHour = "24"
			}

			hourInt, err := strconv.Atoi(updateIntervalInHour)
			if err != nil {
				time.Sleep(24 * time.Hour)
			} else {
				select {
				case <-time.After(time.Duration(hourInt) * time.Hour):
					// Continue loop after sleep
				case <-ctx.Done():
					return // Exit loop if context is cancelled
				}
			}
		}
	}
}

func main() {
	// Context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	redisClient := database.InitializeRedis()
	if err := redisClient.Ping(context.Background()).Err(); err != nil {
		log.Fatalf("Failed to connect to Redis: %s\n", err)
	}

	err := database.InitializeSQLite()
	if err != nil {
		log.Fatalf("Error initializing SQLite database: %v", err)

	}
	index := 1
	for {
		maxConcurrency := 1
		m3uUrl, m3uExists := os.LookupEnv(fmt.Sprintf("M3U_URL_%d", index))
		rawMaxConcurrency, maxConcurrencyExists := os.LookupEnv(fmt.Sprintf("M3U_MAX_CONCURRENCY_%d", index))
		if !m3uExists {
			break
		}

		if maxConcurrencyExists {
			var err error
			maxConcurrency, err = strconv.Atoi(rawMaxConcurrency)
			if err != nil {
				maxConcurrency = 1
			}
		}

		// Start the goroutine for periodic updates
		go updateSource(ctx, m3uUrl, index, maxConcurrency)

		index++
	}

	// HTTP handlers
	http.HandleFunc("/playlist.m3u", m3u.GenerateM3UContent)
	http.HandleFunc("/stream/", mp4Handler)

	// Start the server
	fmt.Println("Server is running on port 8080...")
	fmt.Println("Playlist Endpoint is running (`/playlist.m3u`)")
	fmt.Println("Stream Endpoint is running (`/stream/{streamID}.mp4`)")
	err = http.ListenAndServe(":8080", nil)
	if err != nil {
		log.Fatalf("HTTP server error: %v", err)
	}
}
