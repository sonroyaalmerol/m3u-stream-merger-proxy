package store

import (
	"os"
	"regexp"
	"strings"

	"m3u-stream-merger/utils"

	"github.com/edsrzf/mmap-go"
)

func ParseStreamInfoBySlug(slug string) (*StreamInfo, error) {
	return DecodeSlug(slug)
}

func M3UScanner(m3uIndex int, fn func(streamInfo StreamInfo)) error {
	utils.SafeLogf("Parsing M3U #%d...\n", m3uIndex+1)
	filePath := utils.GetM3UFilePathByIndex(m3uIndex)

	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	// Memory-map the file
	mappedFile, err := mmap.Map(file, mmap.RDONLY, 0)
	if err != nil {
		return err
	}
	defer mappedFile.Unmap()

	// Process the file as a single large string
	content := string(mappedFile)
	lines := strings.Split(content, "\n")

	var currentLine string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#EXTINF:") {
			currentLine = line
		} else if currentLine != "" && !strings.HasPrefix(line, "#") {
			// Parse the stream info
			streamInfo := parseLine(currentLine, line, m3uIndex)
			currentLine = ""

			if !checkFilter(streamInfo) {
				continue
			}

			fn(streamInfo)
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

	currentStream.Slug = EncodeSlug(currentStream)

	if debug {
		utils.SafeLogf("[DEBUG] Generated slug: %s\n", currentStream.Slug)
	}

	return currentStream
}
