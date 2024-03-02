package m3u

import (
	"bufio"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"

	"m3u-stream-merger/database"
)

func ParseM3UFromURL(db *sql.DB, m3uURL string, m3uIndex int, maxConcurrency int) error {
	// Set the custom User-Agent header
	userAgent, userAgentExists := os.LookupEnv("USER_AGENT")
	if !userAgentExists {
		userAgent = "IPTV Smarters/1.0.3 (iPad; iOS 16.6.1; Scale/2.00)"
	}

	// Create a new HTTP client with a custom User-Agent header
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Follow redirects while preserving the custom User-Agent header
			req.Header.Set("User-Agent", userAgent)
			return nil
		},
	}

	log.Printf("Parsing M3U from URL: %s\n", m3uURL)

	resp, err := client.Get(m3uURL)
	if err != nil {
		return fmt.Errorf("HTTP GET error: %v", err)
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)

	var currentStream database.StreamInfo

	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "#EXTINF:") {
			currentStream = database.StreamInfo{}

			// Define a regular expression to capture key-value pairs
			regex := regexp.MustCompile(`(\S+?)="([^"]*?)"`)

			// Find all key-value pairs in the line
			matches := regex.FindAllStringSubmatch(line, -1)

			for _, match := range matches {
				key := strings.TrimSpace(match[1])
				value := strings.TrimSpace(match[2])

				switch key {
				case "tvg-id":
					currentStream.TvgID = value
				case "tvg-name":
					currentStream.Title = value
				case "group-title":
					currentStream.Group = value
				case "tvg-logo":
					currentStream.LogoURL = value
				}
			}
		} else if strings.HasPrefix(line, "#EXTVLCOPT:") {
			// Extract logo URL from #EXTVLCOPT line
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				currentStream.LogoURL = parts[1]
			}
		} else if strings.HasPrefix(line, "http") {
			existingStream, err := database.GetStreamByTitle(db, currentStream.Title)
			if err != nil {
				return fmt.Errorf("GetStreamByTitle error (title: %s): %v", currentStream.Title, err)
			}

			var dbId int64
			if existingStream.Title != currentStream.Title {
				log.Printf("Creating new database entry: %s", currentStream.Title)
				dbId, err = database.InsertStream(db, currentStream)
				if err != nil {
					return fmt.Errorf("InsertStream error (title: %s): %v", currentStream.Title, err)
				}
			} else {
				log.Printf("Using existing database entry: %s", currentStream.Title)
				dbId = existingStream.DbId
			}

			log.Printf("Adding MP4 url entry to %s: %s", currentStream.Title, line)
			_, err = database.InsertStreamUrl(db, dbId, database.StreamURL{
				Content:        line,
				M3UIndex:       m3uIndex,
				MaxConcurrency: maxConcurrency,
			})
			if err != nil {
				return fmt.Errorf("InsertStreamUrl error (title: %s): %v", currentStream.Title, err)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanner error: %v", err)
	}

	return nil
}
