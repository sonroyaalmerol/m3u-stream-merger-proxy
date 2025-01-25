package handlers

import (
//	"bytes"
	"io"
	"log"
	"os/exec"
	"net/http"
	"os"
	"strings"
	"fmt"
	"m3u-stream-merger/utils"

)

func FfmpegHandler(w http.ResponseWriter, r *http.Request, _url string) {
	debug := os.Getenv("DEBUG") == "true"
	_ffm_input := os.Getenv("FFMPEG_IN_ARGS")
	_ffm_output := os.Getenv("FFMPEG_OUT_ARGS")

	if debug {
		utils.SafeLogf("[DEBUG] Setting up: %v\n", _url)
	}

	// setup the command to run
	_cmd_args := fmt.Sprintf(
		"-re %s -i %s -c copy -copyts -f mpegts %s pipe:1",
		_ffm_input,
		_url,
		_ffm_output )

	if debug {
		utils.SafeLogf("[DEBUG] FFMpeg CMD Args: %v\n", _cmd_args)
	}

	// Prepare FFmpeg command
	cmd := exec.Command("/usr/local/bin/ffmpeg", strings.Fields( _cmd_args )[0:]... )

	// Set up a pipe to capture FFmpeg's output
	cmdOut, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("Error creating stdout pipe: %v", err)
		
	}

	// Start FFmpeg process
	if err := cmd.Start(); err != nil {
		log.Printf("Error starting FFmpeg: %v", err)
		
	}

	// Set response headers for live streaming (MPEG-TS format)
	w.Header().Set("Content-Type", "video/MP2T") // Set appropriate content type for MPEG-TS
	w.Header().Set("Transfer-Encoding", "chunked")

	// Stream the data from FFmpeg's output to the HTTP response in chunks
	buffer := make([]byte, 1024) // Set buffer size: was 4096
	for {
		n, err := cmdOut.Read(buffer)
		if err == io.EOF {
			break // End of stream
		}
		if err != nil {
			log.Printf("Error reading FFmpeg output: %v", err)
			break
		}

		// Write the chunk to the HTTP response
		_, writeErr := w.Write(buffer[:n])
		if writeErr != nil {
			log.Printf("Error writing to response: %v", writeErr)
			break
		}

		// Ensure the client gets data in real-time by flushing the response
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}

	// Wait for FFmpeg to finish processing
	if err := cmd.Wait(); err != nil {
		log.Printf("FFmpeg exited with error: %v", err)

	}

	log.Println("Streaming finished.")

}
