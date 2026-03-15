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
		userAgent, exists := os.LookupEnv("USER_AGENT")
		if !exists {
			userAgent = "IPTV Smarters/1.0.3 (iPad; iOS 16.6.1; Scale/2.00)"
		}
		return userAgent
	case "HTTP_ACCEPT":
		accept, exists := os.LookupEnv("HTTP_ACCEPT")
		if !exists {
			accept = "video/MP2T, */*"
		}
		return accept
	default:
		return ""
	}
}

var (
	m3uIndexes     []string
	m3uIndexesOnce = new(sync.Once)
)

func GetM3UIndexes() []string {
	m3uIndexesOnce.Do(func() {
		for _, env := range os.Environ() {
			pair := strings.SplitN(env, "=", 2)
			if after, ok := strings.CutPrefix(pair[0], "M3U_URL_"); ok {
				indexString := after
				m3uIndexes = append(m3uIndexes, indexString)
			}
		}
	})
	return m3uIndexes
}

var (
	epgIndexes     []string
	epgIndexesOnce = new(sync.Once)
)

func GetEPGIndexes() []string {
	epgIndexesOnce.Do(func() {
		for _, env := range os.Environ() {
			pair := strings.SplitN(env, "=", 2)
			if after, ok := strings.CutPrefix(pair[0], "EPG_URL_"); ok {
				indexString := after
				epgIndexes = append(epgIndexes, indexString)
			}
		}
	})
	return epgIndexes
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
		if after, ok := strings.CutPrefix(pair[0], prefix); ok {
			// Remove the prefix (e.g. "FILTER_")
			indexStr := after
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

	epgIndexesOnce = new(sync.Once)
	epgIndexes = nil

	filterMutex.Lock()
	filters = make(map[string][]string)
	filterMutex.Unlock()
}
