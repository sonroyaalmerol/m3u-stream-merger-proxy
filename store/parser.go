package store

import (
	"bufio"
	"os"
	"regexp"
	"strings"

	"m3u-stream-merger/utils"

	"github.com/gosimple/slug"
)

func ParseStreamInfoBySlug(slug string) (*StreamInfo, error) {
	return DecodeSlug(slug)
}

func M3UScanner(m3uIndex int, fn func(streamInfo StreamInfo, lineNumber int)) error {
	utils.SafeLogln("Parsing M3U file.")
	filePath := utils.GetM3UFilePathByIndex(m3uIndex)

	file, err := os.Open(filePath)
	if err != nil {
		return err
	}

	scanner := bufio.NewScanner(file)

	currentLine := 0
	for scanner.Scan() {
		currentLine++
		line := scanner.Text()
		if strings.HasPrefix(line, "#EXTINF:") {
			if scanner.Scan() {
				nextLine := scanner.Text()
				// skip all other #EXT tags
				for strings.HasPrefix(nextLine, "#") {
					if scanner.Scan() {
						nextLine = scanner.Text()
					} else {
						break
					}
				}

				streamInfo := parseLine(line, nextLine, m3uIndex)

				if !checkFilter(streamInfo) {
					continue
				}

				fn(streamInfo, currentLine)
			}
		}
	}

	return nil
}

func parseLine(line string, nextLine string, m3uIndex int) StreamInfo {
	debug := os.Getenv("DEBUG") == "true"
	if debug {
		utils.SafeLogf("[DEBUG] Parsing line: %s\n", line)
		utils.SafeLogf("[DEBUG] Next line: %s\n", nextLine)
		utils.SafeLogf("[DEBUG] M3U index: %d\n", m3uIndex)
	}

	currentStream := StreamInfo{}
	currentStream.URLs = map[int]string{m3uIndex: strings.TrimSpace(nextLine)}

	lineWithoutPairs := line

	// Define a regular expression to capture key-value pairs
	regex := regexp.MustCompile(`([a-zA-Z0-9_-]+)="([^"]+)"`)

	// Find all key-value pairs in the line
	matches := regex.FindAllStringSubmatch(line, -1)

	for _, match := range matches {
		key := strings.TrimSpace(match[1])
		value := strings.TrimSpace(match[2])

		if debug {
			utils.SafeLogf("[DEBUG] Processing attribute: %s=%s\n", key, value)
		}

		switch strings.ToLower(key) {
		case "tvg-id":
			currentStream.TvgID = utils.TvgIdParser(value)
		case "tvg-chno":
			currentStream.TvgChNo = utils.TvgChNoParser(value)
		case "tvg-name":
			currentStream.Title = utils.TvgNameParser(value)
		case "group-title":
			currentStream.Group = utils.GroupTitleParser(value)
		case "tvg-logo":
			currentStream.LogoURL = utils.TvgLogoParser(value)
		default:
			if debug {
				utils.SafeLogf("[DEBUG] Uncaught attribute: %s=%s\n", key, value)
			}
		}

		lineWithoutPairs = strings.Replace(lineWithoutPairs, match[0], "", 1)
	}

	lineCommaSplit := strings.SplitN(lineWithoutPairs, ",", 2)

	if len(lineCommaSplit) > 1 {
		if debug {
			utils.SafeLogf("[DEBUG] Line comma split detected, title: %s\n", strings.TrimSpace(lineCommaSplit[1]))
		}
		currentStream.Title = utils.TvgNameParser(strings.TrimSpace(lineCommaSplit[1]))
	}

	currentStream.Slug = slug.Make(currentStream.Title)

	if debug {
		utils.SafeLogf("[DEBUG] Generated slug: %s\n", currentStream.Slug)
	}

	return currentStream
}
