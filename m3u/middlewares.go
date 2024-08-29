package m3u

import (
	"m3u-stream-merger/utils"
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
			utils.SafeLogf("Error compiling character filter regex: %v\n", err)
		} else {
			value = re.ReplaceAllString(value, "")
		}
	}

	return generalParser(value)
}

func tvgIdParser(value string) string {
	return generalParser(value)
}

func tvgChNoParser(value string) string {
	return generalParser(value)
}

func groupTitleParser(value string) string {
	return generalParser(value)
}

func tvgLogoParser(value string) string {
	return generalParser(value)
}

func checkIncludeGroup(groups []string, line string) bool {
	debug := os.Getenv("DEBUG") == "true"
	if debug {
		utils.SafeLogf("[DEBUG] Checking if line includes group: %s\n", line)
	}

	if len(groups) == 0 {
		return true
	} else {
		for _, group := range groups {
			toMatch := "group-title=" + "\"" + group + "\""
			if strings.Contains(strings.ToLower(line), toMatch) {
				if debug {
					utils.SafeLogf("[DEBUG] Line matches group: %s\n", group)
				}
				return true
			}
		}
		return false
	}
}
