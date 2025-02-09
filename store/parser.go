package store

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"m3u-stream-merger/config"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/utils"

	"github.com/edsrzf/mmap-go"
	"golang.org/x/crypto/sha3"
)

var attributeRegex = regexp.MustCompile(`([a-zA-Z0-9_-]+)="([^"]+)"`)

func ParseStreamInfoBySlug(slug string) (*StreamInfo, error) {
	initInfo, err := DecodeSlug(slug)
	if err != nil {
		return nil, err
	}

	initInfo.URLs = make(map[string]map[string]string)

	indexes := utils.GetM3UIndexes()

	for _, m3uIndex := range indexes {
		safeTitle := base64.StdEncoding.EncodeToString([]byte(initInfo.Title))

		fileName := fmt.Sprintf("%s_%s*", safeTitle, m3uIndex)
		globPattern := filepath.Join(config.GetStreamsDirPath(), "*", fileName)

		fileMatches, err := filepath.Glob(globPattern)
		if err != nil {
			logger.Default.Debugf("Error finding files for pattern %s: %v", globPattern, err)
			continue
		}

		if _, exists := initInfo.URLs[m3uIndex]; !exists {
			initInfo.URLs[m3uIndex] = make(map[string]string)
		}

		for _, fileMatch := range fileMatches {
			fileNameSplit := strings.Split(filepath.Base(fileMatch), "|")
			if len(fileNameSplit) != 2 {
				continue
			}

			urlEncoded, err := os.ReadFile(fileMatch)
			if err != nil {
				continue
			}

			url, err := base64.StdEncoding.DecodeString(string(urlEncoded))
			if err != nil {
				continue
			}

			initInfo.URLs[m3uIndex][fileNameSplit[1]] = strings.TrimSpace(string(url))
		}
	}

	return initInfo, nil
}

func M3UScanner(m3uIndex string, sessionId string, fn func(streamInfo *StreamInfo)) error {
	logger.Default.Logf("Parsing M3U #%s...", m3uIndex)
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
			streamInfo := parseLine(sessionId, currentLine, line, m3uIndex)
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

func parseLine(sessionId string, line string, nextLine string, m3uIndex string) *StreamInfo {
	logger.Default.Debugf("Parsing line: %s", line)
	logger.Default.Debugf("Next line: %s", nextLine)
	logger.Default.Debugf("M3U index: %s", m3uIndex)

	cleanUrl := strings.TrimSpace(nextLine)

	currentStream := StreamInfo{}

	lineWithoutPairs := line

	// Define a regular expression to capture key-value pairs

	// Find all key-value pairs in the line
	matches := attributeRegex.FindAllStringSubmatch(line, -1)

	for _, match := range matches {
		key := strings.TrimSpace(match[1])
		value := strings.TrimSpace(match[2])

		logger.Default.Debugf("Processing attribute: %s=%s", key, value)

		switch strings.ToLower(key) {
		case "tvg-id":
			currentStream.TvgID = utils.TvgIdParser(value)
		case "tvg-chno":
			fallthrough
		case "channel-id":
			fallthrough
		case "channel-number":
			currentStream.TvgChNo = utils.TvgChNoParser(value)
		case "tvg-name":
			currentStream.Title = utils.TvgNameParser(value)
		case "tvg-type":
			currentStream.TvgType = utils.TvgTypeParser(value)
		case "tvg-group":
			fallthrough
		case "group-title":
			currentStream.Group = utils.GroupTitleParser(value)
		case "tvg-logo":
			currentStream.LogoURL = utils.TvgLogoParser(value)
		default:
			logger.Default.Debugf("Uncaught attribute: %s=%s", key, value)
		}

		lineWithoutPairs = strings.Replace(lineWithoutPairs, match[0], "", 1)
	}

	lineCommaSplit := strings.SplitN(lineWithoutPairs, ",", 2)

	if len(lineCommaSplit) > 1 {
		logger.Default.Debugf("Line comma split detected, title: %s", strings.TrimSpace(lineCommaSplit[1]))
		currentStream.Title = utils.TvgNameParser(strings.TrimSpace(lineCommaSplit[1]))
	}

	encodedUrl := base64.StdEncoding.EncodeToString([]byte(cleanUrl))

	sessionDirPath := filepath.Join(config.GetStreamsDirPath(), sessionId)

	if currentStream.Title == "" {
		logger.Default.Debugf("Stream missing title, skipping: %s", line)
		return nil
	}

	err := os.MkdirAll(sessionDirPath, os.ModePerm)
	if err != nil {
		logger.Default.Debugf("Error creating stream cache folder: %s -> %v", sessionDirPath, err)
	}

	base64Title := base64.StdEncoding.EncodeToString([]byte(currentStream.Title))
	h := sha3.Sum224([]byte(cleanUrl))
	urlHash := hex.EncodeToString(h[:])
	fileName := fmt.Sprintf("%s_%s|%s", base64Title, m3uIndex, urlHash)
	filePath := filepath.Join(sessionDirPath, fileName)

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		if err := os.WriteFile(filePath, []byte(encodedUrl), 0644); err != nil {
			logger.Default.Debugf("Error indexing stream: %s (#%s) -> %v", currentStream.Title, m3uIndex, err)
		}
		// Initialize maps if not already initialized
		if currentStream.URLs == nil {
			currentStream.URLs = make(map[string]map[string]string)
		}
		if currentStream.URLs[m3uIndex] == nil {
			currentStream.URLs[m3uIndex] = make(map[string]string)
		}

		// Add the URL to the map
		currentStream.URLs[m3uIndex][urlHash] = cleanUrl
	}

	return &currentStream
}
