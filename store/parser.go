package store

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"m3u-stream-merger/utils"

	"github.com/edsrzf/mmap-go"
)

const streamsDirPath = "/m3u-proxy/data/streams"

func ParseStreamInfoBySlug(slug string) (*StreamInfo, error) {
	debug := os.Getenv("DEBUG") == "true"

	initInfo, err := DecodeSlug(slug)
	if err != nil {
		return nil, err
	}

	initInfo.URLs = make(map[string]map[string]string)

	indexes := utils.GetM3UIndexes()

	for _, m3uIndex := range indexes {
		safeTitle := base64.StdEncoding.EncodeToString([]byte(initInfo.Title))

		fileName := fmt.Sprintf("%s_%s*", safeTitle, m3uIndex)
		globPattern := filepath.Join(streamsDirPath, "*", fileName)

		fileMatches, err := filepath.Glob(globPattern)
		if err != nil {
			if debug {
				utils.SafeLogf("Error finding files for pattern %s: %v", globPattern, err)
			}
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

func M3UScanner(m3uIndex string, fn func(streamInfo StreamInfo)) error {
	utils.SafeLogf("Parsing M3U #%s...\n", m3uIndex)
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

	sessionIdHash := md5.Sum([]byte(time.Now().String()))
	sessionId := hex.EncodeToString(sessionIdHash[:])

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

	// entries, err := os.ReadDir(streamsDirPath)
	// if err != nil {
	// 	return fmt.Errorf("error reading dir path: %w", err)
	// }

	// for _, e := range entries {
	// 	if e.Name() == sessionId {
	// 		continue
	// 	}

	// 	_ = os.RemoveAll(filepath.Join(streamsDirPath, e.Name()))
	// }

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading M3U file: %w", err)
	}

	return nil
}

func parseLine(sessionId string, line string, nextLine string, m3uIndex string) StreamInfo {
	debug := os.Getenv("DEBUG") == "true"
	if debug {
		utils.SafeLogf("[DEBUG] Parsing line: %s\n", line)
		utils.SafeLogf("[DEBUG] Next line: %s\n", nextLine)
		utils.SafeLogf("[DEBUG] M3U index: %s\n", m3uIndex)
	}

	cleanUrl := strings.TrimSpace(nextLine)

	currentStream := StreamInfo{}

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

	encodedUrl := base64.StdEncoding.EncodeToString([]byte(cleanUrl))

	sessionDirPath := filepath.Join(streamsDirPath, sessionId)

	err := os.MkdirAll(sessionDirPath, os.ModePerm)
	if err != nil {
		utils.SafeLogf("[DEBUG] Error creating stream cache folder: %s -> %v\n", sessionDirPath, err)
	}

	for i := 0; true; i++ {
		fileName := fmt.Sprintf("%s_%s|%d", base64.StdEncoding.EncodeToString([]byte(currentStream.Title)), m3uIndex, i)
		filePath := filepath.Join(sessionDirPath, fileName)

		if _, err := os.Stat(filePath); errors.Is(err, os.ErrNotExist) {
			err = os.WriteFile(filePath, []byte(encodedUrl), 0644)
			if err != nil {
				utils.SafeLogf("[DEBUG] Error indexing stream: %s (#%s) -> %v\n", currentStream.Title, m3uIndex, err)
			}

			// Initialize maps if not already initialized
			if currentStream.URLs == nil {
				currentStream.URLs = make(map[string]map[string]string)
			}
			if currentStream.URLs[m3uIndex] == nil {
				currentStream.URLs[m3uIndex] = make(map[string]string)
			}

			// Add the URL to the map
			currentStream.URLs[m3uIndex][strconv.Itoa(i)] = cleanUrl
			break
		}
	}

	return currentStream
}
