package m3u

import (
	"m3u-stream-merger/database"
	"m3u-stream-merger/utils"
	"regexp"
)

func checkFilter(stream database.StreamInfo) bool {
	excludeTitleFilters := utils.GetFilters("EXCLUDE_TITLE")
	includeTitleFilters := utils.GetFilters("INCLUDE_TITLE")
	excludeGroupFilters := utils.GetFilters("EXCLUDE_GROUPS")
	includeGroupFilters := utils.GetFilters("INCLUDE_GROUPS")

	if allFiltersEmpty(excludeTitleFilters, includeTitleFilters, excludeGroupFilters, includeGroupFilters) {
		return true
	}

	if matchAny(includeGroupFilters, stream.Group) || matchAny(includeTitleFilters, stream.Title) {
		return true
	}

	if matchAny(excludeGroupFilters, stream.Group) || matchAny(excludeTitleFilters, stream.Title) {
		return false
	}

	// If there are only include filters and none matched, return false
	return len(includeGroupFilters) == 0 && len(includeTitleFilters) == 0
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
