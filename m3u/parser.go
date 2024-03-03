package m3u

import (
	"bufio"
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"m3u-stream-merger/database"
)

func ParseM3UFromURL(db *sql.DB, m3uURL string, m3uIndex int, maxConcurrency int) error {
	// Set the custom User-Agent header
	userAgent, userAgentExists := os.LookupEnv("USER_AGENT")
	if !userAgentExists {
		userAgent = "IPTV Smarters/1.0.3 (iPad; iOS 16.6.1; Scale/2.00)"
	}

	maxRetries := 10
	var err error
	maxRetriesStr, maxRetriesExists := os.LookupEnv("MAX_RETRIES")
	if !maxRetriesExists {
		maxRetries, err = strconv.Atoi(maxRetriesStr)
		if err != nil {
			maxRetries = 10
		}
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

	for i := 0; i <= maxRetries; i++ {
		resp, err := client.Get(m3uURL)
		if err != nil {
			return fmt.Errorf("HTTP GET error: %v", err)
		}
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)

		var currentStream database.StreamInfo

		for scanner.Scan() {
			line := scanner.Text()
			extInfLine := ""

			if strings.HasPrefix(line, "#EXTINF:") {
				currentStream = database.StreamInfo{}
				extInfLine = line

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

					switch key {
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
			} else if strings.HasPrefix(line, "#EXTVLCOPT:") {
				// Extract logo URL from #EXTVLCOPT line
				parts := strings.SplitN(line, "=", 2)
				if len(parts) == 2 {
					if os.Getenv("DEBUG") == "true" {
						log.Printf("Uncaught attribute (#EXTVLCOPT): %s=%s\n", parts[0], parts[1])
					}
				}
			} else if strings.HasPrefix(line, "http") {
				if len(strings.TrimSpace(currentStream.Title)) == 0 {
					log.Printf("Error capturing title, line will be skipped: %s\n", extInfLine)
					continue
				}

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
					log.Printf("Adding MP4 url entry to %s: %s\n", currentStream.Title, line)
				}

				existingUrl, err := database.GetStreamUrlByUrlAndIndex(db, line, m3uIndex)
				if err != nil {
					return fmt.Errorf("GetStreamUrlByUrlAndIndex error (url: %s): %v", line, err)
				}

				if existingUrl.Content != line || existingUrl.M3UIndex != m3uIndex {
					_, err = database.InsertStreamUrl(db, dbId, database.StreamURL{
						Content:        line,
						M3UIndex:       m3uIndex,
						MaxConcurrency: maxConcurrency,
					})
				}

				if err != nil {
					return fmt.Errorf("InsertStreamUrl error (title: %s): %v", currentStream.Title, err)
				}
			}
		}

		if scanner.Err() == io.EOF {
			// Unexpected EOF, retry
			log.Printf("Unexpected EOF. Retrying in 5 secs... (url: %s)\n", m3uURL)
			time.Sleep(5 * time.Second)
			continue
		}

		if err := scanner.Err(); err != nil {
			return fmt.Errorf("scanner error: %v", err)
		}

		return nil
	}
	return fmt.Errorf("Max retries reached without success. Failed to fetch %s\n", m3uURL)
}
