package m3u

import (
	"bufio"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"m3u-stream-merger/database"
)

func parseM3UFile(m3uURL string, m3uIndex int) (error) {
	fmt.Printf("Parsing M3U from URL: %s\n", m3uURL)

  resp, err := http.Get(m3uURL)
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
        currentStream.TvgID = ""
        currentStream.Title = ""
        currentStream.Group = ""
				currentStream.LogoURL = parts[1]
			}
		} else if strings.HasPrefix(line, "http") {
      existingStream, err := database.GetStreamByTitle(currentStream.Title) 
      if err != nil {
        return fmt.Errorf("GetStreamByTitle error (title: %s): %v", currentStream.Title, err)
      }

      var dbId int64
      if existingStream.Title != currentStream.Title {
        dbId, err = database.InsertStream(currentStream)
        if err != nil {
          return fmt.Errorf("InsertStream error (title: %s): %v", currentStream.Title, err)
        }
      } else {
        dbId = existingStream.DbId
      }

      _, err = database.InsertStreamUrl(dbId, database.StreamURL{
        Content:  line,
        M3UIndex: m3uIndex,
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
