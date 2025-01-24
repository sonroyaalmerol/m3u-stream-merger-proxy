package utils

import (
	"os"
	"regexp"
	"strings"
)

func GeneralParser(value string) string {
	if strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
		value = strings.Trim(value, `"`)
	}

	return value
}

func TvgNameParser(value string) string {
	substrFilter := os.Getenv("TITLE_SUBSTR_FILTER")
	// Apply character filter
	if substrFilter != "" {
		re, err := regexp.Compile(substrFilter)
		if err != nil {
			SafeLogf("Error compiling character filter regex: %v\n", err)
		} else {
			value = re.ReplaceAllString(value, "")
		}
	}

	return GeneralParser(value)
}

func TvgIdParser(value string) string {
	return GeneralParser(value)
}

func TvgChNoParser(value string) string {
	return GeneralParser(value)
}

func TvgTypeParser(value string) string {
	return GeneralParser(value)
}

func GroupTitleParser(value string) string {
	return GeneralParser(value)
}

func TvgLogoParser(value string) string {
	return GeneralParser(value)
}
