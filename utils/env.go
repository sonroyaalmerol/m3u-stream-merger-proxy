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
		userAgent, userAgentExists := os.LookupEnv("USER_AGENT")
		if !userAgentExists {
			userAgent = "IPTV Smarters/1.0.3 (iPad; iOS 16.6.1; Scale/2.00)"
		}
		return userAgent
	default:
		return ""
	}
}

var m3uIndexes []string
var m3uIndexesInitialized bool

func GetM3UIndexes() []string {
	if m3uIndexesInitialized {
		return m3uIndexes
	}
	m3uIndexes = []string{}
	for _, env := range os.Environ() {
		pair := strings.SplitN(env, "=", 2)
		if strings.HasPrefix(pair[0], "M3U_URL_") {
			indexString := strings.TrimPrefix(pair[0], "M3U_URL_")
			m3uIndexes = append(m3uIndexes, indexString)
		}
	}
	m3uIndexesInitialized = true
	return m3uIndexes
}

var (
	filters            = make(map[string][]string)
	filtersInitialized = make(map[string]bool)
	filterMutex        sync.RWMutex
)

func GetFilters(baseEnv string) []string {
	filterMutex.RLock()
	if filtersInitialized[baseEnv] {
		result := filters[baseEnv]
		filterMutex.RUnlock()
		return result
	}
	filterMutex.RUnlock()

	filterMutex.Lock()
	defer filterMutex.Unlock()

	envFilters := []string{}
	for _, env := range os.Environ() {
		pair := strings.SplitN(env, "=", 2)
		if strings.HasPrefix(pair[0], baseEnv) {
			indexString := strings.TrimPrefix(pair[0], fmt.Sprintf("%s_", baseEnv))
			_, err := strconv.Atoi(indexString)
			if err != nil {
				continue
			}
			envFilters = append(envFilters, pair[1])
		}
	}
	filtersInitialized[baseEnv] = true
	filters[baseEnv] = envFilters
	return filters[baseEnv]
}
