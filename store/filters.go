package store

import (
	"m3u-stream-merger/logger"
	"m3u-stream-merger/utils"
	"regexp"
)

var (
	includeRegexes     [][]*regexp.Regexp
	excludeRegexes     [][]*regexp.Regexp
	filtersInitialized bool
)

func checkFilter(stream *StreamInfo) bool {
	if !filtersInitialized {
		excludeGroups := utils.GetFilters("EXCLUDE_GROUPS")
		excludeTitle := utils.GetFilters("EXCLUDE_TITLE")
		includeGroups := utils.GetFilters("INCLUDE_GROUPS")
		includeTitle := utils.GetFilters("INCLUDE_TITLE")

		excludeRegexes = [][]*regexp.Regexp{
			compileRegexes(excludeGroups),
			compileRegexes(excludeTitle),
		}
		includeRegexes = [][]*regexp.Regexp{
			compileRegexes(includeGroups),
			compileRegexes(includeTitle),
		}
		filtersInitialized = true
	}

	if allFiltersEmpty() {
		return true
	}

	if matchAny(includeRegexes[0], stream.Group) || matchAny(includeRegexes[1], stream.Title) {
		return true
	}

	if matchAny(excludeRegexes[0], stream.Group) || matchAny(excludeRegexes[1], stream.Title) {
		return false
	}

	return len(includeRegexes[0]) == 0 && len(includeRegexes[1]) == 0
}

func compileRegexes(filters []string) []*regexp.Regexp {
	var regexes []*regexp.Regexp
	for _, f := range filters {
		re, err := regexp.Compile(f)
		if err != nil {
			logger.Default.Debugf("Error compiling regex %s: %v", f, err)
			continue
		}
		regexes = append(regexes, re)
	}
	return regexes
}

func matchAny(regexes []*regexp.Regexp, s string) bool {
	for _, re := range regexes {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

func allFiltersEmpty() bool {
	for _, res := range includeRegexes {
		if len(res) > 0 {
			return false
		}
	}
	for _, res := range excludeRegexes {
		if len(res) > 0 {
			return false
		}
	}
	return true
}
