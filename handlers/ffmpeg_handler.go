package handlers

import (
	"io"
	"os/exec"
	"net/http"
	"os"
	"strings"
	"fmt"
	"context"
	"sync"
	"m3u-stream-merger/utils"

)

func FfmpegHandler(w http.ResponseWriter, r *http.Request, _url string, resp *http.Response, statusChan chan int) {
	debug := os.Getenv("DEBUG") == "true"
	_ffm_input := os.Getenv("FFMPEG_IN_ARGS")
	_ffm_output := os.Getenv("FFMPEG_OUT_ARGS")

	// Set response headers for live streaming (MPEG-TS format)
	w.Header().Set("Content-Type", "video/MP2T") // Set appropriate content type for MPEG-TS
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Transfer-Encoding", "chunked")

	if debug {
		utils.SafeLogf("[DEBUG] Setting up: %v\n", _url)
	}

	// setup the command to run
	_cmd_args := fmt.Sprintf(
		"%s -i pipe:0 -c copy -copyts -f mpegts %s pipe:1",
		_ffm_input,
		_ffm_output )

	if debug {
		utils.SafeLogf("[DEBUG] FFMpeg CMD Args: %v\n", _cmd_args)
	}

	// Prepare FFmpeg command
	cmd := exec.Command("/usr/local/bin/ffmpeg", strings.Fields( _cmd_args )[0:]... )

	// Get a pipe to the stdin of the ffmpeg process
	cmdIn, err := cmd.StdinPipe()
	if err != nil {
		utils.SafeLogf("Error creating stdin pipe: %v\n", err)
		statusChan <- 5
		cmd.Process.Kill( ) 
	}

	// Set up a pipe to capture FFmpeg's output
	cmdOut, err := cmd.StdoutPipe()
	if err != nil {
		utils.SafeLogf("Error creating stdout pipe: %v", err)
		statusChan <- 5
		cmd.Process.Kill( )
	}

	// Start FFmpeg process
	if err := cmd.Start(); err != nil {
		utils.SafeLogf("Error starting FFmpeg: %v", err)
		statusChan <- 5
		cmd.Process.Kill( )
	}

	// Cancel the process if the client disconnects
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		<-ctx.Done() // Wait for context cancellation
		cmd.Process.Kill()
		utils.SafeLogf("Client disconnected, FFmpeg process killed")
	}()

	// Stream data from the remote source to FFmpeg's stdin
	go func() {
		defer cmdIn.Close()
		if _, err := io.Copy(cmdIn, resp.Body); err != nil {
			utils.SafeLogf("Error streaming to FFmpeg: %v\n", err)
			cancel() // Cancel context to stop processing
		}
	}()
	
	// setup a heap for the output buffer
	var bufferPool = sync.Pool{
		New: func() interface{} {
			return make([]byte, 16384) // Increased buffer size: 1024, 2048, 4096, 8192
		},
	}

	// Stream FFmpeg's output to the HTTP response
	//buffer := make([]byte, 16384) // Increased buffer size: 1024, 2048, 4096, 8192
	for {

		// In the function:
		buffer := bufferPool.Get().([]byte) // Fetch buffer from pool
		defer bufferPool.Put(buffer)       // Return buffer to pool when done

		n, err := cmdOut.Read(buffer)
		if err == io.EOF {
			statusChan <- 2 // End of stream
			break
		}
		if err != nil {
			utils.SafeLogf("Error reading FFmpeg output: %v", err)
			statusChan <- 5 
			break
		}

		_, writeErr := w.Write(buffer[:n])
		if writeErr != nil {
			utils.SafeLogf("Error writing to response: %v", writeErr)
			statusChan <- 5 
			cancel() // Cancel context to stop processing
			break
		}

		// Flush data to the client in real-time
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}

	}

	/*
	// Stream data from the remote source to ffmpeg's stdin
	go func() {
		defer cmdIn.Close()
		if _, err := io.Copy(cmdIn, resp.Body); err != nil {
			utils.SafeLogf("Error streaming to ffmpeg: %v\n", err)
			statusChan <- 5
			cmd.Process.Kill( )
		}
	}()

	// Stream the data from FFmpeg's output to the HTTP response in chunks
	buffer := make([]byte, 8192) // Set buffer size: was 4096
	for {
		n, err := cmdOut.Read(buffer)
		if err == io.EOF {
			statusChan <- 2 // End of stream
			cmd.Process.Kill( )
		}
		if err != nil {
			utils.SafeLogf("Error reading FFmpeg output: %v", err)
			statusChan <- 5
			cmd.Process.Kill( )
		}

		// Write the chunk to the HTTP response
		_, writeErr := w.Write(buffer[:n])
		if writeErr != nil {
			utils.SafeLogf("Error writing to response: %v", writeErr)
			statusChan <- 5
			cmd.Process.Kill( )
		}

		// Ensure the client gets data in real-time by flushing the response
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}

	}
	*/

	// Wait for FFmpeg to finish processing
	if err := cmd.Wait(); err != nil {
		utils.SafeLogf("FFmpeg exited with error: %v", err)
		statusChan <- 5
		cmd.Process.Kill( )
	}

}
