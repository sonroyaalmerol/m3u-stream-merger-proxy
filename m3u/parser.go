package m3u

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"

	"m3u-stream-merger/database"
)

func parseM3UFile(filePath string, m3uIndex int) (error) {
	fmt.Printf("Parsing: %s\n", filePath)

	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("Open error: %v", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)

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
