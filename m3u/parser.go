package m3u

import (
	"bufio"
	"bytes"
	"fmt"
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
		utils.SafeLogf("[DEBUG] Parsing line: %s\n", line)
		utils.SafeLogf("[DEBUG] Next line: %s\n", nextLine)
		utils.SafeLogf("[DEBUG] M3U index: %d\n", m3uIndex)
	}

	currentStream := database.StreamInfo{}
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
		currentStream.Title = tvgNameParser(strings.TrimSpace(lineCommaSplit[1]))
	}

	currentStream.Slug = slug.Make(currentStream.Title)

	if debug {
		utils.SafeLogf("[DEBUG] Generated slug: %s\n", currentStream.Slug)
	}

	return currentStream
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
		utils.SafeLogf("[DEBUG] Max retries set to %d\n", maxRetries)
	}

	var buffer bytes.Buffer
	for i := 0; i <= maxRetries; i++ {
		if debug {
			utils.SafeLogf("[DEBUG] Attempt %d to download M3U\n", i+1)
		}
		err := downloadM3UToBuffer(m3uURL, &buffer)
		if err != nil {
			utils.SafeLogf("downloadM3UToBuffer error. Retrying in 5 secs... (error: %v)\n", err)
			time.Sleep(5 * time.Second)
			continue
		}

		utils.SafeLogln("Parsing downloaded M3U file.")
		scanner := bufio.NewScanner(&buffer)
		var wg sync.WaitGroup

		parserWorkers := os.Getenv("PARSER_WORKERS")
		if strings.TrimSpace(parserWorkers) == "" {
			parserWorkers = "5"
		}

		if debug {
			utils.SafeLogf("[DEBUG] Using %s parser workers\n", parserWorkers)
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
						utils.SafeLogf("[DEBUG] Worker processing stream info: %s\n", streamInfo.Slug)
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
				utils.SafeLogf("M3U Parser error: %v\n", err)
			}
		}()

		for scanner.Scan() {
			line := scanner.Text()

			if debug {
				utils.SafeLogf("[DEBUG] Scanning line: %s\n", line)
			}

			if strings.HasPrefix(line, "#EXTINF:") {
				if scanner.Scan() {
					nextLine := scanner.Text()
					for strings.HasPrefix(nextLine, "#") {
						if scanner.Scan() {
							nextLine = scanner.Text()
						} else {
							break
						}
					}

					if debug {
						utils.SafeLogf("[DEBUG] Found next line for EXTINF: %s\n", nextLine)
					}

					streamInfo := parseLine(line, nextLine, m3uIndex)

					if checkFilter(streamInfo) {
						utils.SafeLogf("Stream Info: %v\n", streamInfo)
						streamInfoCh <- streamInfo
					} else {
						if debug {
							utils.SafeLogf("[DEBUG] Skipping due to filter: %s\n", streamInfo.Title)
						}
					}
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
			utils.SafeLogln("[DEBUG] Buffer reset and memory freed")
		}

		return nil
	}

	return fmt.Errorf("Max retries reached without success. Failed to fetch %s\n", m3uURL)
}
