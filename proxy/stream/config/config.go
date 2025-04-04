package config

import (
	"os"
	"strconv"
	"time"
)

type StreamConfig struct {
	SharedBufferSize int
	ChunkSize        int
	TimeoutSeconds   int
	InitialBackoff   time.Duration
	MaxRetries       int
}

func NewDefaultStreamConfig() *StreamConfig {
	finalBufferSize := 8
	finalTimeoutSeconds := 3
	finalMaxRetries := 5

	maxRetries, ok := os.LookupEnv("MAX_RETRIES")
	if ok {
		intMaxRetries, err := strconv.Atoi(maxRetries)
		if err == nil {
			finalMaxRetries = intMaxRetries
		}
	}

	bufferSize, ok := os.LookupEnv("BUFFER_CHUNK_NUM")
	if ok {
		intBufferSize, err := strconv.Atoi(bufferSize)
		if err == nil && intBufferSize >= 0 {
			finalBufferSize = intBufferSize
		}
	}

	streamTimeout, ok := os.LookupEnv("STREAM_TIMEOUT")
	if ok {
		intStreamTimeout, err := strconv.Atoi(streamTimeout)
		if err == nil && intStreamTimeout >= 0 {
			finalTimeoutSeconds = intStreamTimeout
		}
	}

	return &StreamConfig{
		SharedBufferSize: finalBufferSize,
		ChunkSize:        1024 * 1024,
		TimeoutSeconds:   finalTimeoutSeconds,
		InitialBackoff:   200 * time.Millisecond,
		MaxRetries:       finalMaxRetries,
	}
}
