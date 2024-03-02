package main

import (
	"context"
	"fmt"
	"m3u-stream-merger/m3u"
	"net/http"
	"os"
	"strconv"
	"time"
)

func updateSource(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			updateIntervalInHour, exists := os.LookupEnv("UPDATE_INTERVAL")
			if !exists {
				updateIntervalInHour = "24"
			}

			hourInt, err := strconv.Atoi(updateIntervalInHour)
			if err != nil {
			time.Sleep(24 * time.Hour)
			} else {
				time.Sleep(time.Duration(hourInt) * time.Hour) // Adjust the update interval as needed
			}

			// Reload M3U files
			// err = m3u.GetStreams(true)
			// if err != nil {
			// 	fmt.Printf("Error updating M3U: %v\n", err)
			// }
		}
	}
}

func main() {
	// Context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the goroutine for periodic updates
	go updateSource(ctx)

	// HTTP handlers
	http.HandleFunc("/playlist.m3u", m3u.GenerateM3UContent)
	http.HandleFunc("/stream/", mp4Handler)

	// Start the server
	fmt.Println("Server is running on port 8080...")
	http.ListenAndServe(":8080", nil)
}
