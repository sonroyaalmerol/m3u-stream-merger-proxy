package utils

import (
	"fmt"
	"os"
	"strconv"
	"strings"
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

var m3uIndexes []int
var m3uIndexesInitialized bool

func GetM3UIndexes() []int {
	if m3uIndexesInitialized {
		return m3uIndexes
	}
	m3uIndexes = []int{}
	for _, env := range os.Environ() {
		pair := strings.SplitN(env, "=", 2)
		if strings.HasPrefix(pair[0], "M3U_URL_") {
			indexString := strings.TrimPrefix(pair[0], "M3U_URL_")
			index, err := strconv.Atoi(indexString)
			if err != nil {
				continue
			}
			m3uIndexes = append(m3uIndexes, index-1)
		}
	}
	m3uIndexesInitialized = true
	return m3uIndexes
}

var filters []string
var filtersInitialized bool

func GetFilters(baseEnv string) []string {
	if filtersInitialized {
		return filters
	}
	filters = []string{}
	for _, env := range os.Environ() {
		pair := strings.SplitN(env, "=", 2)
		if strings.HasPrefix(pair[0], baseEnv) {
			indexString := strings.TrimPrefix(pair[0], fmt.Sprintf("%s_", baseEnv))
			_, err := strconv.Atoi(indexString)
			if err != nil {
				continue
			}
			filters = append(filters, pair[1])
		}
	}
	filtersInitialized = true
	return filters
}

var customPathsByTitle map[string]string
var customPathsByTitleInitialized bool

func GetCustomPathsByTitle() map[string]string {
	if customPathsByTitleInitialized {
		return customPathsByTitle
	}
	customPathsByTitle = map[string]string{}
	for _, env := range os.Environ() {
		pair := strings.SplitN(env, "=", 2)
		if strings.HasPrefix(pair[0], "CUSTOM_PATH_TITLE_") {
			path := strings.ToLower(strings.TrimPrefix(pair[0], "CUSTOM_PATH_TITLE_"))
			customPathsByTitle[path] = pair[1]
		}
	}
	customPathsByTitleInitialized = true
	return customPathsByTitle
}

var customPathsByGroup map[string]string
var customPathsByGroupInitialized bool

func GetCustomPathsByGroup() map[string]string {
	if customPathsByGroupInitialized {
		return customPathsByGroup
	}
	customPathsByGroup = map[string]string{}
	for _, env := range os.Environ() {
		pair := strings.SplitN(env, "=", 2)
		if strings.HasPrefix(pair[0], "CUSTOM_PATH_GROUP_") {
			path := strings.ToLower(strings.TrimPrefix(pair[0], "CUSTOM_PATH_GROUP_"))
			customPathsByGroup[path] = pair[1]
		}
	}
	customPathsByGroupInitialized = true
	return customPathsByGroup
}
