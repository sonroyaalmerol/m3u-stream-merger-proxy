package m3u

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"m3u-stream-merger/database"
	"m3u-stream-merger/utils"

	"github.com/gosimple/slug"
)

func parseLine(line string, nextLine string, m3uIndex int) database.StreamInfo {
	debug := os.Getenv("DEBUG") == "true"
	if debug {
		utils.SafeLogPrintf(nil, nil, "[DEBUG] Parsing line: %s\n", line)
		utils.SafeLogPrintf(nil, &nextLine, "[DEBUG] Next line: %s\n", nextLine)
		utils.SafeLogPrintf(nil, nil, "[DEBUG] M3U index: %d\n", m3uIndex)
	}

	var currentStream database.StreamInfo
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
			utils.SafeLogPrintf(nil, nil, "[DEBUG] Processing attribute: %s=%s\n", key, value)
		}

		switch strings.ToLower(key) {
		case "tvg-id":
			currentStream.TvgID = tvgIdParser(value)
		case "tvg-chno":
			currentStream.TvgChNo = tvgChNoParser(value)
		case "tvg-name":
			currentStream.Title = tvgNameParser(value)
		case "group-title":
			currentStream.Group = groupTitleParser(value)
		case "tvg-logo":
			currentStream.LogoURL = tvgLogoParser(value)
		default:
			if debug {
				utils.SafeLogPrintf(nil, nil, "[DEBUG] Uncaught attribute: %s=%s\n", key, value)
			}
		}

		lineWithoutPairs = strings.Replace(lineWithoutPairs, match[0], "", 1)
	}

	lineCommaSplit := strings.SplitN(lineWithoutPairs, ",", 2)

	if len(lineCommaSplit) > 1 {
		if debug {
			utils.SafeLogPrintf(nil, nil, "[DEBUG] Line comma split detected, title: %s\n", strings.TrimSpace(lineCommaSplit[1]))
		}
		currentStream.Title = tvgNameParser(strings.TrimSpace(lineCommaSplit[1]))
	}

	currentStream.Slug = slug.Make(currentStream.Title)

	if debug {
		utils.SafeLogPrintf(nil, nil, "[DEBUG] Generated slug: %s\n", currentStream.Slug)
	}

	return currentStream
}

func checkIncludeGroup(groups []string, line string) bool {
	debug := os.Getenv("DEBUG") == "true"
	if debug {
		utils.SafeLogPrintf(nil, nil, "[DEBUG] Checking if line includes group: %s\n", line)
	}

	if len(groups) == 0 {
		return true
	} else {
		for _, group := range groups {
			toMatch := "group-title=" + "\"" + group + "\""
			if strings.Contains(line, toMatch) {
				if debug {
					utils.SafeLogPrintf(nil, nil, "[DEBUG] Line matches group: %s\n", group)
				}
				return true
			}
		}
		return false
	}
}

func downloadM3UToBuffer(m3uURL string, buffer *bytes.Buffer) error {
	debug := os.Getenv("DEBUG") == "true"

	var err error
	maxRetries := 5
	maxRetriesStr, ok := os.LookupEnv("MAX_RETRIES")
	if ok {
		maxRetries, err = strconv.Atoi(maxRetriesStr)
		if err != nil {
			maxRetries = 5
		}
	}

	const retryDelay = time.Second * 5 // Delay between retries

	if debug {
		utils.SafeLogPrintf(nil, &m3uURL, "[DEBUG] Downloading M3U from: %s\n", m3uURL)
	}

	var tempFilePath string
	var finalFilePath string
	startOffset := int64(0)

	// Function to handle actual download with support for resumption
	downloadToTempFile := func() error {
		var err error
		var resp *http.Response

		rangeHeader := ""
		if startOffset > 0 {
			rangeHeader = fmt.Sprintf("bytes=%d-", startOffset)
		}

		resp, err = utils.CustomHttpRequest("GET", m3uURL, rangeHeader)
		if err != nil {
			return fmt.Errorf("HTTP GET error: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			return fmt.Errorf("Failed to download M3U: HTTP status %d", resp.StatusCode)
		}

		tempFile, err := os.OpenFile(tempFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return fmt.Errorf("Error opening temp file: %v", err)
		}
		defer tempFile.Close()

		written, err := io.Copy(tempFile, resp.Body)
		if err != nil {
			return fmt.Errorf("Error writing to temp file: %v", err)
		}

		startOffset += written
		return nil
	}

	if strings.HasPrefix(m3uURL, "file://") {
		// Handle local file directly
		localPath := strings.TrimPrefix(m3uURL, "file://")
		utils.SafeLogPrintf(nil, &localPath, "[DEBUG] Reading M3U from local file: %s\n", localPath)
		finalFilePath = localPath
	} else {
		fileName := filepath.Base(m3uURL)
		tempFilePath := fmt.Sprintf("/tmp/%s.m3u-incomplete", fileName)

		// Retry logic
		for attempt := 1; attempt <= maxRetries; attempt++ {
			err := downloadToTempFile()
			if err == nil {
				break // Success, exit loop
			}

			if debug {
				utils.SafeLogPrintf(nil, &m3uURL, "[DEBUG] Download attempt %d failed: %v\n", attempt, err)
			}
			time.Sleep(retryDelay)
		}

		if startOffset == 0 {
			return fmt.Errorf("Failed to download M3U after %d attempts", maxRetries)
		}

		finalFilePath = strings.TrimSuffix(tempFilePath, "-incomplete")
		if err := os.Rename(tempFilePath, finalFilePath); err != nil {
			return fmt.Errorf("Error renaming temp file: %v", err)
		}
	}

	file, err := os.Open(finalFilePath)
	if err != nil {
		return fmt.Errorf("Error opening file: %v", err)
	}
	defer file.Close()

	_, err = io.Copy(buffer, file)
	if err != nil {
		return fmt.Errorf("Error reading file to buffer: %v", err)
	}

	if debug {
		log.Println("[DEBUG] Successfully read M3U content into buffer")
	}

	return nil
}

func ParseM3UFromURL(db *database.Instance, m3uURL string, m3uIndex int) error {
	debug := os.Getenv("DEBUG") == "true"

	maxRetries := 10
	var err error
	maxRetriesStr, maxRetriesExists := os.LookupEnv("MAX_RETRIES")
	if maxRetriesExists {
		maxRetries, err = strconv.Atoi(maxRetriesStr)
		if err != nil {
			maxRetries = 10
		}
	}

	if debug {
		utils.SafeLogPrintf(nil, nil, "[DEBUG] Max retries set to %d\n", maxRetries)
	}

	var buffer bytes.Buffer
	var grps []string

	includeGroups := os.Getenv(fmt.Sprintf("INCLUDE_GROUPS_%d", m3uIndex+1))
	if includeGroups != "" {
		grps = strings.Split(includeGroups, ",")
		if debug {
			utils.SafeLogPrintf(nil, nil, "[DEBUG] Include groups: %v\n", grps)
		}
	}

	for i := 0; i <= maxRetries; i++ {
		if debug {
			utils.SafeLogPrintf(nil, nil, "[DEBUG] Attempt %d to download M3U\n", i+1)
		}
		err := downloadM3UToBuffer(m3uURL, &buffer)
		if err != nil {
			utils.SafeLogPrintf(nil, nil, "downloadM3UToBuffer error. Retrying in 5 secs... (error: %v)\n", err)
			time.Sleep(5 * time.Second)
			continue
		}

		log.Println("Parsing downloaded M3U file.")
		scanner := bufio.NewScanner(&buffer)
		var wg sync.WaitGroup

		parserWorkers := os.Getenv("PARSER_WORKERS")
		if parserWorkers != "" {
			parserWorkers = "5"
		}

		if debug {
			utils.SafeLogPrintf(nil, nil, "[DEBUG] Using %s parser workers\n", parserWorkers)
		}

		streamInfoCh := make(chan database.StreamInfo)
		errCh := make(chan error)
		numWorkers, err := strconv.Atoi(parserWorkers)
		if err != nil {
			numWorkers = 5
		}

		var streamInfos []database.StreamInfo
		var mu sync.Mutex

		for w := 0; w < numWorkers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for streamInfo := range streamInfoCh {
					if debug {
						utils.SafeLogPrintf(nil, nil, "[DEBUG] Worker processing stream info: %s\n", streamInfo.Slug)
					}
					mu.Lock()
					streamInfos = append(streamInfos, streamInfo)
					mu.Unlock()
				}
			}()
		}

		go func() {
			for err := range errCh {
				utils.SafeLogPrintf(nil, &m3uURL, "M3U Parser error: %v\n", err)
			}
		}()

		for scanner.Scan() {
			line := scanner.Text()

			if debug {
				utils.SafeLogPrintf(nil, nil, "[DEBUG] Scanning line: %s\n", line)
			}

			if strings.HasPrefix(line, "#EXTINF:") && checkIncludeGroup(grps, line) {
				if scanner.Scan() {
					nextLine := scanner.Text()

					if debug {
						utils.SafeLogPrintf(nil, nil, "[DEBUG] Found next line for EXTINF: %s\n", nextLine)
					}

					streamInfo := parseLine(line, nextLine, m3uIndex)
					streamInfoCh <- streamInfo
				}
			}
		}

		close(streamInfoCh)
		wg.Wait()
		close(errCh)

		if err := scanner.Err(); err != nil {
			return fmt.Errorf("scanner error: %v", err)
		}

		if len(streamInfos) > 0 {
			if debug {
				utils.SafeLogPrintf(nil, nil, "[DEBUG] Saving %d stream infos to database\n", len(streamInfos))
			}
			if err := db.SaveToDb(streamInfos); err != nil {
				return fmt.Errorf("failed to save data to database: %v", err)
			}
		}

		buffer.Reset()

		if debug {
			log.Println("[DEBUG] Buffer reset and memory freed")
		}

		return nil
	}

	return fmt.Errorf("Max retries reached without success. Failed to fetch %s\n", m3uURL)
}
