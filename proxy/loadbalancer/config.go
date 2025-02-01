package loadbalancer

import (
	"os"
	"strconv"
)

type LBConfig struct {
	MaxRetries int
	RetryWait  int
}

func NewDefaultLBConfig() *LBConfig {
	finalMaxRetries := 5
	finalRetryWait := 0

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

	return &LBConfig{
		MaxRetries: finalMaxRetries,
		RetryWait:  finalRetryWait,
	}
}
