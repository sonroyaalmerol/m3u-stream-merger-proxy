package m3u

import (
	"log"
	"os"
	"regexp"
	"strings"
)

func generalParser(value string) string {
	if strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
		value = strings.Trim(value, `"`)
	}

	return value
}

func tvgNameParser(value string) string {
	substrFilter := os.Getenv("TITLE_SUBSTR_FILTER")
	// Apply character filter
	if substrFilter != "" {
		re, err := regexp.Compile(substrFilter)
		if err != nil {
			log.Println("Error compiling character filter regex:", err)
		} else {
			value = re.ReplaceAllString(value, "")
		}
	}

	return generalParser(value)
}

func tvgIdParser(value string) string {
	return generalParser(value)
}

func groupTitleParser(value string) string {
	return generalParser(value)
}

func tvgLogoParser(value string) string {
	return generalParser(value)
}
