package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"m3u-stream-merger/database"
	"m3u-stream-merger/m3u"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

var db *sql.DB

func swapDb() error {
	// Generate a unique temporary name
	tempName := fmt.Sprintf("temp_%d", time.Now().UnixNano())

	// Rename the current database to a temporary name
	err := database.RenameSQLite("current_streams", tempName)
	if err != nil {
		return fmt.Errorf("Error renaming current_streams to temp: %v\n", err)
	}

	// Rename the next database to current
	err = database.RenameSQLite("next_streams", "current_streams")
	if err != nil {
		// If renaming fails, revert the previous renaming to maintain consistency
		revertErr := database.RenameSQLite(tempName, "current_streams")
		if revertErr != nil {
			return fmt.Errorf("Error renaming back to current_streams: %v\n", revertErr)
		}
		return fmt.Errorf("Error renaming next_streams to current_streams: %v\n", err)
	}

	// Initialize the new current database
	db, err = database.InitializeSQLite("current_streams")
	if err != nil {
		// If initialization fails, revert both renamings
		revertErr := database.RenameSQLite(tempName, "current_streams")
		if revertErr != nil {
			return fmt.Errorf("Error renaming back to current_streams: %v\n", revertErr)
		}
		revertErr = database.RenameSQLite("current_streams", "next_streams")
		if revertErr != nil {
			return fmt.Errorf("Error renaming back to next_streams: %v\n", revertErr)
		}
		return fmt.Errorf("Error initializing current_streams: %v\n", err)
	}

	// Delete the temporary database
	err = database.DeleteSQLite(tempName)
	if err != nil {
		// Log the error but do not return as this is not a critical error
		fmt.Printf("Error deleting temp database: %v\n", err)
	}

	return nil
}

func updateSource(nextDb *sql.DB, m3uUrl string, index int, maxConcurrency int) {
	log.Printf("Background process: Updating M3U #%d from %s\n", index, m3uUrl)
	err := m3u.ParseM3UFromURL(nextDb, m3uUrl, index, maxConcurrency)
	if err != nil {
		log.Printf("Background process: Error updating M3U: %v\n", err)
	} else {
		log.Printf("Background process: Updated M3U #%d from %s\n", index, m3uUrl)
	}
}

func updateSources(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			var err error
			nextDb, err := database.InitializeSQLite("next_streams")
			if err != nil {
				log.Fatalf("Error initializing next SQLite database: %v", err)
			}

			log.Println("Background process: Checking M3U_URLs...")
			var wg sync.WaitGroup
			index := 1
			for {
				maxConcurrency := 1
				m3uUrl, m3uExists := os.LookupEnv(fmt.Sprintf("M3U_URL_%d", index))
				rawMaxConcurrency, maxConcurrencyExists := os.LookupEnv(fmt.Sprintf("M3U_MAX_CONCURRENCY_%d", index))
				if !m3uExists {
					break
				}

				log.Printf("Background process: Checking M3U_MAX_CONCURRENCY_%d...\n", index)
				if maxConcurrencyExists {
					var err error
					maxConcurrency, err = strconv.Atoi(rawMaxConcurrency)
					if err != nil {
						maxConcurrency = 1
					}
				}

				log.Printf("Background process: Fetching M3U_URL_%d...\n", index)
				wg.Add(1)
				// Start the goroutine for periodic updates
				go func(nextDb *sql.DB, m3uUrl string, index int, maxConcurrency int) {
					defer wg.Done()
					updateSource(nextDb, m3uUrl, index, maxConcurrency)
				}(nextDb, m3uUrl, index, maxConcurrency)

				index++
			}
			wg.Wait()

			err = swapDb()
			if err != nil {
				log.Fatalf("swapDb: %v", err)
			}
			log.Println("Background process: Updated M3U database.")

			updateIntervalInHour, exists := os.LookupEnv("UPDATE_INTERVAL")
			if !exists {
				updateIntervalInHour = "24"
			}

			hourInt, err := strconv.Atoi(updateIntervalInHour)
			if err != nil {
				log.Println("Background process: Sleeping for 24 hours...")
				time.Sleep(24 * time.Hour)
			} else {
				log.Printf("Background process: Sleeping for %d hours...\n", hourInt)
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

	db, err := database.InitializeSQLite("current_streams")
	if err != nil {
		log.Fatalf("Error initializing current SQLite database: %v", err)
	}

	redisClient := database.InitializeRedis()
	if err := redisClient.Ping(context.Background()).Err(); err != nil {
		log.Fatalf("Failed to connect to Redis: %s\n", err)
	}

	go updateSources(ctx)

	// HTTP handlers
	http.HandleFunc("/playlist.m3u", func(w http.ResponseWriter, r *http.Request) {
		m3u.GenerateM3UContent(w, r, db)
	})
	http.HandleFunc("/stream/", func(w http.ResponseWriter, r *http.Request) {
		mp4Handler(w, r, db)
	})

	// Start the server
	log.Println("Server is running on port 8080...")
	log.Println("Playlist Endpoint is running (`/playlist.m3u`)")
	log.Println("Stream Endpoint is running (`/stream/{streamID}.mp4`)")
	err = http.ListenAndServe(":8080", nil)
	if err != nil {
		log.Fatalf("HTTP server error: %v", err)
	}
}
