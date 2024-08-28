package m3u

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"m3u-stream-merger/database"
	"m3u-stream-merger/utils"

	"github.com/gosimple/slug"
)

type Parser struct {
	sync.Mutex
	Streams map[string]*database.StreamInfo
}

func InitializeParser() *Parser {
	return &Parser{
		Streams: map[string]*database.StreamInfo{},
	}
}

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
			if strings.Contains(strings.ToLower(line), toMatch) {
				if debug {
					utils.SafeLogPrintf(nil, nil, "[DEBUG] Line matches group: %s\n", group)
				}
				return true
			}
		}
		return false
	}
}

func downloadM3UToBuffer(m3uURL string, buffer *bytes.Buffer) (err error) {
	debug := os.Getenv("DEBUG") == "true"
	if debug {
		utils.SafeLogPrintf(nil, &m3uURL, "[DEBUG] Downloading M3U from: %s\n", m3uURL)
	}

	var file io.Reader

	if strings.HasPrefix(m3uURL, "file://") {
		localPath := strings.TrimPrefix(m3uURL, "file://")
		utils.SafeLogPrintf(nil, &localPath, "Reading M3U from local file: %s\n", localPath)

		localFile, err := os.Open(localPath)
		if err != nil {
			return fmt.Errorf("Error opening file: %v", err)
		}
		defer localFile.Close()

		file = localFile
	} else {
		utils.SafeLogPrintf(nil, &m3uURL, "Downloading M3U from URL: %s\n", m3uURL)
		resp, err := utils.CustomHttpRequest("GET", m3uURL)
		if err != nil {
			return fmt.Errorf("HTTP GET error: %v", err)
		}

		defer func() {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}()

		file = resp.Body
	}

	_, err = io.Copy(buffer, file)
	if err != nil {
		return fmt.Errorf("Error reading file: %v", err)
	}

	if debug {
		log.Println("[DEBUG] Successfully copied M3U content to buffer")
	}

	return nil
}

func (instance *Parser) GetStreams() []*database.StreamInfo {
	storeArray := []*database.StreamInfo{}
	storeValues := maps.Values(instance.Streams)
	if storeValues != nil {
		storeArray = slices.Collect(storeValues)
	}

	return storeArray
}

func (instance *Parser) ParseURL(m3uURL string, m3uIndex int) error {
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
		if strings.TrimSpace(parserWorkers) == "" {
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

		for w := 0; w < numWorkers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for streamInfo := range streamInfoCh {
					if debug {
						utils.SafeLogPrintf(nil, nil, "[DEBUG] Worker processing stream info: %s\n", streamInfo.Slug)
					}

					instance.Lock()
					_, ok := instance.Streams[streamInfo.Title]
					if !ok {
						instance.Streams[streamInfo.Title] = &streamInfo
					} else {
						if instance.Streams[streamInfo.Title].URLs == nil {
							instance.Streams[streamInfo.Title].URLs = map[int]string{}
						}

						if streamInfo.URLs != nil {
							maps.Copy(instance.Streams[streamInfo.Title].URLs, streamInfo.URLs)
						}
					}
					instance.Unlock()
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
					log.Println(streamInfo)
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

		buffer.Reset()

		if debug {
			log.Println("[DEBUG] Buffer reset and memory freed")
		}

		return nil
	}

	return fmt.Errorf("Max retries reached without success. Failed to fetch %s\n", m3uURL)
}
