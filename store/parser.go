package store

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"m3u-stream-merger/utils"

	"github.com/edsrzf/mmap-go"
)

const streamsDirPath = "/m3u-proxy/data/streams"

func ParseStreamInfoBySlug(slug string) (*StreamInfo, error) {
	initInfo, err := DecodeSlug(slug)
	if err != nil {
		return nil, err
	}

	initInfo.URLs = make(map[int]string)

	indexes := utils.GetM3UIndexes()

	for _, m3uIndex := range indexes {
		fileName := fmt.Sprintf("%s_%d", base64.StdEncoding.EncodeToString([]byte(initInfo.Title)), m3uIndex)
		urlEncoded, err := os.ReadFile(filepath.Join(streamsDirPath, fileName))
		if err != nil {
			continue
		}

		url, err := base64.StdEncoding.DecodeString(string(urlEncoded))
		if err != nil {
			continue
		}

		initInfo.URLs[m3uIndex] = strings.TrimSpace(string(url))
	}

	return initInfo, nil
}

func M3UScanner(m3uIndex int, fn func(streamInfo StreamInfo)) error {
	utils.SafeLogf("Parsing M3U #%d...\n", m3uIndex+1)
	filePath := utils.GetM3UFilePathByIndex(m3uIndex)

	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	mappedFile, err := mmap.Map(file, mmap.RDONLY, 0)
	if err != nil {
		return err
	}
	defer func() {
		_ = mappedFile.Unmap()
	}()

	scanner := bufio.NewScanner(bytes.NewReader(mappedFile))
	var currentLine string

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#EXTINF:") {
			currentLine = line
		} else if currentLine != "" && !strings.HasPrefix(line, "#") {
			streamInfo := parseLine(currentLine, line, m3uIndex)
			currentLine = ""

			if checkFilter(streamInfo) {
				fn(streamInfo)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading M3U file: %w", err)
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

	cleanUrl := strings.TrimSpace(nextLine)

	currentStream := StreamInfo{}
	currentStream.URLs = map[int]string{m3uIndex: cleanUrl}

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

	fileName := fmt.Sprintf("%s_%d", base64.StdEncoding.EncodeToString([]byte(currentStream.Title)), m3uIndex)
	encodedUrl := base64.StdEncoding.EncodeToString([]byte(cleanUrl))

	err := os.MkdirAll(streamsDirPath, os.ModePerm)
	if err != nil {
		utils.SafeLogf("[DEBUG] Error creating stream cache folder: %s -> %v\n", streamsDirPath, err)
	}
	err = os.WriteFile(filepath.Join(streamsDirPath, fileName), []byte(encodedUrl), 0644)
	if err != nil {
		utils.SafeLogf("[DEBUG] Error indexing stream: %s (#%d) -> %v\n", currentStream.Title, m3uIndex+1, err)
	}

	return currentStream
}
