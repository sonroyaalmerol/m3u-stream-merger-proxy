package store

import (
	"m3u-stream-merger/utils"
	"regexp"
)

var includeFilters [][]string
var excludeFilters [][]string
var filtersInitialized bool

func checkFilter(stream *StreamInfo) bool {
	if !filtersInitialized {
		excludeFilters = [][]string{
			utils.GetFilters("EXCLUDE_GROUPS"),
			utils.GetFilters("EXCLUDE_TITLE"),
		}
		includeFilters = [][]string{
			utils.GetFilters("INCLUDE_GROUPS"),
			utils.GetFilters("INCLUDE_TITLE"),
		}
		filtersInitialized = true
	}

	if allFiltersEmpty(append(excludeFilters, includeFilters...)...) {
		return true
	}

	if matchAny(includeFilters[0], stream.Group) || matchAny(includeFilters[1], stream.Title) {
		return true
	}

	if matchAny(excludeFilters[0], stream.Group) || matchAny(excludeFilters[1], stream.Title) {
		return false
	}

	// If there are only include filters and none matched, return false
	return len(includeFilters[0]) == 0 && len(includeFilters[1]) == 0
}

func allFiltersEmpty(filters ...[]string) bool {
	for _, f := range filters {
		if len(f) > 0 {
			return false
		}
	}
	return true
}

func matchAny(filters []string, text string) bool {
	for _, filter := range filters {
		if matched, _ := regexp.MatchString(filter, text); matched {
			return true
		}
	}
	return false
}
