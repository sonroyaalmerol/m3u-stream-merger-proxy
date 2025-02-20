package loadbalancer

import (
	"os"
	"strconv"
)

type LBConfig struct {
	MaxRetries  int
	RetryWait   int
	BufferChunk int
}

func NewDefaultLBConfig() *LBConfig {
	finalMaxRetries := 5
	finalRetryWait := 0
	finalBufferChunk := 1024 * 1024

	maxRetries, ok := os.LookupEnv("MAX_RETRIES")
	if ok {
		intMaxRetries, err := strconv.Atoi(maxRetries)
		if err == nil {
			finalMaxRetries = intMaxRetries
		}
	}

	retryWait, ok := os.LookupEnv("RETRY_WAIT")
	if ok {
		intRetryWait, err := strconv.Atoi(retryWait)
		if err == nil {
			finalRetryWait = intRetryWait
		}
	}

	bufferSize, ok := os.LookupEnv("BUFFER_CHUNK_NUM")
	if ok {
		intBufferSize, err := strconv.Atoi(bufferSize)
		if err == nil && intBufferSize >= 0 {
			finalBufferChunk = intBufferSize * 1024 * 1024
		}
	}

	return &LBConfig{
		MaxRetries:  finalMaxRetries,
		RetryWait:   finalRetryWait,
		BufferChunk: finalBufferChunk,
	}
}
