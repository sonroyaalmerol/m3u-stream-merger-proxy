package m3u

import (
	"bufio"
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"m3u-stream-merger/database"
	"m3u-stream-merger/utils"
)

func parseLine(line string, nextLine string, m3uIndex int) database.StreamInfo {
	var currentStream database.StreamInfo
	currentStream.URLs = []database.StreamURL{{
		Content:  strings.TrimSpace(nextLine),
		M3UIndex: m3uIndex,
	}}

	lineWithoutPairs := line

	// Define a regular expression to capture key-value pairs
	regex := regexp.MustCompile(`([a-zA-Z0-9_-]+)=("[^"]+"|[^",]+)`)

	// Find all key-value pairs in the line
	matches := regex.FindAllStringSubmatch(line, -1)

	for _, match := range matches {
		key := strings.TrimSpace(match[1])
		value := strings.TrimSpace(match[2])

		if strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
			value = strings.Trim(value, `"`)
		}

		switch strings.ToLower(key) {
		case "tvg-id":
			currentStream.TvgID = value
		case "tvg-name":
			currentStream.Title = value
		case "group-title":
			currentStream.Group = value
		case "tvg-logo":
			currentStream.LogoURL = value
		default:
			if os.Getenv("DEBUG") == "true" {
				log.Printf("Uncaught attribute: %s=%s\n", key, value)
			}
		}

		var pair string
		if strings.Contains(value, `"`) || strings.Contains(value, ",") {
			// If the value contains double quotes or commas, format it as key="value"
			pair = fmt.Sprintf(`%s="%s"`, key, value)
		} else {
			// Otherwise, format it as key=value
			pair = fmt.Sprintf(`%s=%s`, key, value)
		}
		lineWithoutPairs = strings.Replace(lineWithoutPairs, pair, "", 1)
	}

	lineCommaSplit := strings.SplitN(lineWithoutPairs, ",", 2)

	if len(lineCommaSplit) > 1 {
		currentStream.Title = strings.TrimSpace(lineCommaSplit[1])
	}

	return currentStream
}

func insertStreamToDb(db *sql.DB, currentStream database.StreamInfo) error {
	existingStream, err := database.GetStreamByTitle(db, currentStream.Title)
	if err != nil {
		return fmt.Errorf("GetStreamByTitle error (title: %s): %v", currentStream.Title, err)
	}

	var dbId int64
	if existingStream.Title != currentStream.Title {
		if os.Getenv("DEBUG") == "true" {
			log.Printf("Creating new database entry: %s\n", currentStream.Title)
		}
		dbId, err = database.InsertStream(db, currentStream)
		if err != nil {
			return fmt.Errorf("InsertStream error (title: %s): %v", currentStream.Title, err)
		}
	} else {
		if os.Getenv("DEBUG") == "true" {
			log.Printf("Using existing database entry: %s\n", existingStream.Title)
		}
		dbId = existingStream.DbId
	}

	if os.Getenv("DEBUG") == "true" {
		log.Printf("Adding MP4 url entry to %s\n", currentStream.Title)
	}

	for _, currentStreamUrl := range currentStream.URLs {
		existingUrl, err := database.GetStreamUrlByUrlAndIndex(db, currentStreamUrl.Content, currentStreamUrl.M3UIndex)
		if err != nil {
			return fmt.Errorf("GetStreamUrlByUrlAndIndex error (url: %s): %v", currentStreamUrl.Content, err)
		}

		if existingUrl.Content != currentStreamUrl.Content || existingUrl.M3UIndex != currentStreamUrl.M3UIndex {
			_, err = database.InsertStreamUrl(db, dbId, currentStreamUrl)
			if err != nil {
				return fmt.Errorf("InsertStreamUrl error (title: %s): %v", currentStream.Title, err)
			}
		}
	}

	return nil
}

func downloadM3UToBuffer(m3uURL string, buffer *bytes.Buffer) (err error) {
	userAgent := utils.GetEnv("USER_AGENT")

	// Create a new HTTP client with a custom User-Agent header
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Follow redirects while preserving the custom User-Agent header
			req.Header.Set("User-Agent", userAgent)
			return nil
		},
	}

	// Download M3U for processing
	log.Printf("Downloading M3U from URL: %s\n", m3uURL)
	resp, err := client.Get(m3uURL)
	if err != nil {
		return fmt.Errorf("HTTP GET error: %v", err)
	}
	defer resp.Body.Close()

	_, err = io.Copy(buffer, resp.Body)
	if err != nil {
		return fmt.Errorf("Download file error: %v", err)
	}

	return nil
}

func ParseM3UFromURL(db *sql.DB, m3uURL string, m3uIndex int) error {
	maxRetries := 10
	var err error
	maxRetriesStr, maxRetriesExists := os.LookupEnv("MAX_RETRIES")
	if !maxRetriesExists {
		maxRetries, err = strconv.Atoi(maxRetriesStr)
		if err != nil {
			maxRetries = 10
		}
	}

	var buffer bytes.Buffer

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

		streamInfoCh := make(chan database.StreamInfo)
		errCh := make(chan error)

		for scanner.Scan() {
			line := scanner.Text()

			if strings.HasPrefix(line, "#EXTINF:") {
				if scanner.Scan() {
					wg.Add(2)
					nextLine := scanner.Text()

					// Insert parsed stream to database
					go func(c chan database.StreamInfo) {
						defer wg.Done()
						errCh <- insertStreamToDb(db, <-c)
					}(streamInfoCh)

					// Parse stream lines
					go func(l string, nl string) {
						defer wg.Done()
						streamInfoCh <- parseLine(l, nl, m3uIndex)
					}(line, nextLine)

					// Error handler
					go func() {
						err := <-errCh
						if err != nil {
							log.Printf("M3U Parser error: %v", err)
						}
					}()
				}
			}
		}

		if err := scanner.Err(); err != nil {
			return fmt.Errorf("scanner error: %v", err)
		}

		wg.Wait()
		close(streamInfoCh)
		close(errCh)

		// Free up memory used by buffer
		buffer.Reset()

		return nil
	}

	return fmt.Errorf("Max retries reached without success. Failed to fetch %s\n", m3uURL)
}
