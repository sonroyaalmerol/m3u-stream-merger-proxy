package stream

import (
	"os"
	"strconv"
	"time"
)

type StreamConfig struct {
	BufferSizeMB   int
	TimeoutSeconds int
	InitialBackoff time.Duration
}

func NewDefaultStreamConfig() *StreamConfig {
	finalBufferSize := 0
	finalTimeoutSeconds := 3

	bufferSize, ok := os.LookupEnv("BUFFER_MB")
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
		BufferSizeMB:   finalBufferSize,
		TimeoutSeconds: finalTimeoutSeconds,
		InitialBackoff: 200 * time.Millisecond,
	}
}
