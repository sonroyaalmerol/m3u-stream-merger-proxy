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

func updateSource(ctx context.Context, m3uUrl string, index int) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
      fmt.Printf("Updating M3U #%s from %d\n", m3uUrl, index)
      err := m3u.ParseM3UFromURL(m3uUrl, index)
      if err != nil {
			  fmt.Printf("Error updating M3U: %v\n", err)
      }

      fmt.Printf("Updated M3U #%s from %d\n", m3uUrl, index)

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
		}
	}
}

func main() {
	// Context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	index := 1
	for {
		m3uUrl, m3uExists := os.LookupEnv(fmt.Sprintf("M3U_URL_%d", index))
		if !m3uExists {
			break
		}

    // Start the goroutine for periodic updates
    go updateSource(ctx, m3uUrl, index)

		index++
	}

	// HTTP handlers
	http.HandleFunc("/playlist.m3u", m3u.GenerateM3UContent)
	http.HandleFunc("/stream/", mp4Handler)

	// Start the server
	fmt.Println("Server is running on port 8080...")
	http.ListenAndServe(":8080", nil)
}
