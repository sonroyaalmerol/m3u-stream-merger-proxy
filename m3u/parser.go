package m3u

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
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
	var currentStream database.StreamInfo
	currentStream.URLs = map[int]string{m3uIndex: strings.TrimSpace(nextLine)}

	lineWithoutPairs := line

	// Define a regular expression to capture key-value pairs
	// regex := regexp.MustCompile(`([a-zA-Z0-9_-]+)=("[^"]+"|[^",]+)`)
	regex := regexp.MustCompile(`([a-zA-Z0-9_-]+)="([^"]+)"`)

	// Find all key-value pairs in the line
	matches := regex.FindAllStringSubmatch(line, -1)

	for _, match := range matches {
		key := strings.TrimSpace(match[1])
		value := strings.TrimSpace(match[2])

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
			if os.Getenv("DEBUG") == "true" {
				log.Printf("Uncaught attribute: %s=%s\n", key, value)
			}
		}

		lineWithoutPairs = strings.Replace(lineWithoutPairs, match[0], "", 1)
	}

	lineCommaSplit := strings.SplitN(lineWithoutPairs, ",", 2)

	if len(lineCommaSplit) > 1 {
		currentStream.Title = tvgNameParser(strings.TrimSpace(lineCommaSplit[1]))
	}

	currentStream.Slug = slug.Make(currentStream.Title)

	return currentStream
}

func checkIncludeGroup(groups []string, line string) bool {
	if len(groups) == 0 {
		return true
	} else {
		for _, group := range groups {
			toMatch := "group-title=" + "\"" + group + "\""
			if strings.Contains(line, toMatch) {
				return true
			}
		}
		return false
	}
}

func downloadM3UToBuffer(m3uURL string, buffer *bytes.Buffer) (err error) {
	var file io.Reader

	if strings.HasPrefix(m3uURL, "file://") {
		localPath := strings.TrimPrefix(m3uURL, "file://")
		log.Printf("Reading M3U from local file: %s\n", localPath)

		localFile, err := os.Open(localPath)
		if err != nil {
			return fmt.Errorf("Error opening file: %v", err)
		}
		defer localFile.Close()

		file = localFile
	} else {
		log.Printf("Downloading M3U from URL: %s\n", m3uURL)
		resp, err := utils.CustomHttpRequest("GET", m3uURL)
		if err != nil {
			return fmt.Errorf("HTTP GET error: %v", err)
		}
		defer resp.Body.Close()

		file = resp.Body
	}

	_, err = io.Copy(buffer, file)
	if err != nil {
		return fmt.Errorf("Error reading file: %v", err)
	}

	return nil
}

func ParseM3UFromURL(db *database.Instance, m3uURL string, m3uIndex int) error {
	maxRetries := 10
	var err error
	maxRetriesStr, maxRetriesExists := os.LookupEnv("MAX_RETRIES")
	if maxRetriesExists {
		maxRetries, err = strconv.Atoi(maxRetriesStr)
		if err != nil {
			maxRetries = 10
		}
	}

	var buffer bytes.Buffer
	var grps []string

	includeGroups := os.Getenv(fmt.Sprintf("INCLUDE_GROUPS_%d", m3uIndex+1))
	if includeGroups != "" {
		grps = strings.Split(includeGroups, ",")
	}

	for i := 0; i <= maxRetries; i++ {
		err := downloadM3UToBuffer(m3uURL, &buffer)
		if err != nil {
			log.Printf("downloadM3UToBuffer error. Retrying in 5 secs... (error: %v)\n", err)
			time.Sleep(5 * time.Second)
			continue
		}

		log.Println("Parsing downloaded M3U file.")
		scanner := bufio.NewScanner(&buffer)
		var wg sync.WaitGroup

		// Create channels for workers
		parserWorkers := os.Getenv("PARSER_WORKERS")
		if parserWorkers != "" {
			parserWorkers = "5"
		}
		streamInfoCh := make(chan database.StreamInfo)
		errCh := make(chan error)
		numWorkers, err := strconv.Atoi(parserWorkers)
		if err != nil {
			numWorkers = 5
		}

		// Shared slice to collect parsed data
		var streamInfos []database.StreamInfo
		var mu sync.Mutex

		// Worker pool for parsing
		for w := 0; w < numWorkers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for streamInfo := range streamInfoCh {
					// Collect parsed data
					mu.Lock()
					streamInfos = append(streamInfos, streamInfo)
					mu.Unlock()
				}
			}()
		}

		// Error handler
		go func() {
			for err := range errCh {
				log.Printf("M3U Parser error: %v", err)
			}
		}()

		// Parse lines and send to worker pool
		for scanner.Scan() {
			line := scanner.Text()

			if strings.HasPrefix(line, "#EXTINF:") && checkIncludeGroup(grps, line) {
				if scanner.Scan() {
					nextLine := scanner.Text()

					streamInfo := parseLine(line, nextLine, m3uIndex)
					streamInfoCh <- streamInfo
				}
			}
		}

		// Close channels after processing
		close(streamInfoCh)
		wg.Wait()
		close(errCh)

		if err := scanner.Err(); err != nil {
			return fmt.Errorf("scanner error: %v", err)
		}

		if len(streamInfos) > 0 {
			if err := db.SaveToDb(streamInfos); err != nil {
				return fmt.Errorf("failed to save data to database: %v", err)
			}
		}

		// Free up memory used by buffer
		buffer.Reset()

		return nil
	}

	return fmt.Errorf("Max retries reached without success. Failed to fetch %s\n", m3uURL)
}
