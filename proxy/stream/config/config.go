package config

import (
	"m3u-stream-merger/logger"
	"os"
	"strconv"
	"time"
)

type StreamConfig struct {
	SharedBufferSize   int
	ChunkSize          int
	TimeoutSeconds     int
	InitialBackoff     time.Duration
	MaxRetries         int
	ExpectedThroughput int64
	EnablePCRPacer     bool
}

func NewDefaultStreamConfig() *StreamConfig {
	chunkSize := 1024 * 1024
	finalBufferSize := 8
	finalTimeoutSeconds := 3
	finalMaxRetries := 5
	finalExpectedThroughput := int64(0)
	finalEnablePCRPacer := false

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

	expectedThroughput, ok := os.LookupEnv("MINIMUM_THROUGHPUT")
	if ok {
		intExpectedThroughput, err := strconv.ParseInt(expectedThroughput, 10, 64)
		if err == nil && intExpectedThroughput >= 0 {
			finalExpectedThroughput = intExpectedThroughput
		}
	}

	enablePacer, ok := os.LookupEnv("ENABLE_PCR_PACER")
	if ok {
		if b, err := strconv.ParseBool(enablePacer); err == nil {
			finalEnablePCRPacer = b
		}
	}

	if finalBufferSize < 2 {
		logger.Default.Warnf("BUFFER_CHUNK_NUM must be at least 2; falling back to 2")
		finalBufferSize = 2
	}
	if finalTimeoutSeconds < 1 {
		logger.Default.Warnf("STREAM_TIMEOUT must be at least 1; falling back to 1")
		finalTimeoutSeconds = 1
	}
	if finalExpectedThroughput > 0 &&
		int64(finalTimeoutSeconds)*finalExpectedThroughput > int64(finalBufferSize)*int64(chunkSize) {
		logger.Default.Warnf("STREAM_TIMEOUT (%ds) at MINIMUM_THROUGHPUT (%d Bps) needs more buffered content than BUFFER_CHUNK_NUM=%d chunks hold; clients may freeze before failover completes",
			finalTimeoutSeconds, finalExpectedThroughput, finalBufferSize)
	}

	return &StreamConfig{
		SharedBufferSize:   finalBufferSize,
		ChunkSize:          chunkSize,
		TimeoutSeconds:     finalTimeoutSeconds,
		InitialBackoff:     200 * time.Millisecond,
		MaxRetries:         finalMaxRetries,
		ExpectedThroughput: finalExpectedThroughput,
		EnablePCRPacer:     finalEnablePCRPacer,
	}
}
