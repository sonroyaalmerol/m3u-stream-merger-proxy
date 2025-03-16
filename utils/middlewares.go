package utils

import (
	"encoding/base64"
	"m3u-stream-merger/logger"
	"net/url"
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
			logger.Default.Errorf("Error compiling character filter regex: %v", err)
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
	value = GeneralParser(value)

	parsedURL, err := url.Parse(value)
	if err == nil && (parsedURL.Scheme == "http" || parsedURL.Scheme == "https") &&
		parsedURL.Host != "" {
		encoded := base64.URLEncoding.EncodeToString([]byte(value))
		final, err := url.JoinPath(os.Getenv("BASE_URL"), "/a/"+encoded)
		if err != nil {
			return value
		}
		return final
	}

	return value
}
