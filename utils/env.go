package utils

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
)

func GetEnv(env string) string {
	switch env {
	case "USER_AGENT":
		// Set the custom User-Agent header
		userAgent, exists := os.LookupEnv("USER_AGENT")
		if !exists {
			userAgent = "IPTV Smarters/1.0.3 (iPad; iOS 16.6.1; Scale/2.00)"
		}
		return userAgent
	default:
		return ""
	}
}

var (
	m3uIndexes         []string
	m3uIndexesOnce = new(sync.Once)
)

func GetM3UIndexes() []string {
	m3uIndexesOnce.Do(func() {
		for _, env := range os.Environ() {
			pair := strings.SplitN(env, "=", 2)
			if strings.HasPrefix(pair[0], "M3U_URL_") {
				indexString := strings.TrimPrefix(pair[0], "M3U_URL_")
				m3uIndexes = append(m3uIndexes, indexString)
			}
		}
	})
	return m3uIndexes
}

var (
	filters     = make(map[string][]string)
	filterMutex sync.RWMutex
)

func GetFilters(baseEnv string) []string {
	filterMutex.RLock()
	if cached, ok := filters[baseEnv]; ok {
		filterMutex.RUnlock()
		return cached
	}
	filterMutex.RUnlock()

	filterMutex.Lock()
	defer filterMutex.Unlock()

	if cached, ok := filters[baseEnv]; ok {
		return cached
	}

	var envFilters []string
	prefix := fmt.Sprintf("%s_", baseEnv)
	for _, env := range os.Environ() {
		pair := strings.SplitN(env, "=", 2)
		if strings.HasPrefix(pair[0], prefix) {
			// Remove the prefix (e.g. "FILTER_")
			indexStr := strings.TrimPrefix(pair[0], prefix)
			// Ensure the suffix is an integer.
			if _, err := strconv.Atoi(indexStr); err != nil {
				continue
			}
			envFilters = append(envFilters, pair[1])
		}
	}
	filters[baseEnv] = envFilters
	return envFilters
}

func ResetCaches() {
	m3uIndexesOnce = new(sync.Once)
	m3uIndexes = nil

	filterMutex.Lock()
	filters = make(map[string][]string)
	filterMutex.Unlock()
}
