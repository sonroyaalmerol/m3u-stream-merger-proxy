package main

import (
	"fmt"
	"image"
	"image/jpeg"
	"net/http"
	"os"
	"time"
)

const (
	fps = 24
)

func videoHandler(w http.ResponseWriter) {
	// Set appropriate HTTP headers for streaming MP4
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Transfer-Encoding", "chunked")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Retrieve the path to the image from the environment variable
	imagePath := os.Getenv("EXHAUSTED_IMAGE")
	if imagePath == "" {
		fmt.Println("Environment variable EXHAUSTED_IMAGE is not set")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Open the image file
	file, err := os.Open(imagePath)
	if err != nil {
		fmt.Println("Error opening image file:", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	// Decode the image
	img, _, err := image.Decode(file)
	if err != nil {
		fmt.Println("Error decoding image:", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Infinite loop to continuously write frames
	for {
		// Encode the image as JPEG and write it to the response writer
		if err := jpeg.Encode(w, img, nil); err != nil {
			// If any error occurs (e.g., client disconnects), break the loop
			break
		}

		// Flush the response writer to send the chunk
		w.(http.Flusher).Flush()

		// Sleep for a duration equivalent to the frame rate
		time.Sleep(time.Second / fps)
	}
}
