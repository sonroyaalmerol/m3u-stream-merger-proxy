package sourceproc

import (
	"encoding/base64"
	"fmt"
	"m3u-stream-merger/config"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/utils"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

var (
	filterOnce     sync.Once
	includeRegexes [][]*regexp.Regexp
	excludeRegexes [][]*regexp.Regexp
)

// checkFilter checks if a stream matches the configured filters
func checkFilter(stream *StreamInfo) bool {
	filterOnce.Do(initFilters)

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

func initFilters() {
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
}

func ParseStreamInfoBySlug(slug string) (*StreamInfo, error) {
	initInfo, err := DecodeSlug(slug)
	if err != nil {
		return nil, err
	}

	initInfo.URLs = make(map[string]map[string]string)
	var wg sync.WaitGroup
	errCh := make(chan error, len(utils.GetM3UIndexes()))

	for _, m3uIndex := range utils.GetM3UIndexes() {
		wg.Add(1)
		go func(idx string) {
			defer wg.Done()
			if err := loadStreamURLs(initInfo, idx); err != nil {
				errCh <- err
			}
		}(m3uIndex)
	}

	// Wait for all goroutines and close error channel
	go func() {
		wg.Wait()
		close(errCh)
	}()

	// Collect any errors
	var errors []error
	for err := range errCh {
		errors = append(errors, err)
	}

	if len(errors) > 0 {
		return nil, fmt.Errorf("errors loading stream URLs: %v", errors)
	}

	return initInfo, nil
}

func loadStreamURLs(stream *StreamInfo, m3uIndex string) error {
	safeTitle := base64.StdEncoding.EncodeToString([]byte(stream.Title))
	fileName := fmt.Sprintf("%s_%s*", safeTitle, m3uIndex)
	globPattern := filepath.Join(config.GetStreamsDirPath(), fileName)

	fileMatches, err := filepath.Glob(globPattern)
	if err != nil {
		return fmt.Errorf("error finding files for pattern %s: %v", globPattern, err)
	}

	stream.URLs[m3uIndex] = make(map[string]string)

	for _, fileMatch := range fileMatches {
		fileNameSplit := filepath.Base(fileMatch)
		parts := strings.Split(fileNameSplit, "|")
		if len(parts) != 2 {
			continue
		}

		fileContent, err := os.ReadFile(fileMatch)
		if err != nil {
			logger.Default.Debugf("Error reading file %s: %v", fileMatch, err)
			continue
		}

		encodedUrl := fileContent
		urlIndex := "0"
		splitContent := strings.SplitN(string(fileContent), ":::", 2)
		if len(splitContent) == 2 {
			encodedUrl = []byte(splitContent[1])
			urlIndex = splitContent[0]
		}

		url, err := base64.StdEncoding.DecodeString(string(encodedUrl))
		if err != nil {
			logger.Default.Debugf("Error decoding URL from %s: %v", fileMatch, err)
			continue
		}

		stream.URLs[m3uIndex][parts[1]] = strings.TrimSpace(fmt.Sprintf("%s:::%s", urlIndex, string(url)))
	}

	return nil
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
