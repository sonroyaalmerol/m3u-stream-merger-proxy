package m3u

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"

	"m3u-stream-merger/database"
)

// GetStreams retrieves and merges stream information from multiple M3U files.
func GetStreams(skipClearing bool) error {
	// Initialize database
	err := database.InitializeSQLite()
	if err != nil {
		return fmt.Errorf("InitializeSQLite error: %v", err)
	}

	if !skipClearing {
		// init
		log.Println("Loading from database...")
		fromDB, err := database.LoadFromSQLite()
		if err == nil {
			Streams = fromDB 

			return nil
		}
	}

	err = loadM3UFiles(skipClearing)
	if err != nil {
		return fmt.Errorf("loadM3UFiles error: %v", err)
	}

	var wg sync.WaitGroup
	var mutex sync.Mutex

	for index, path := range m3uFilePaths {
		wg.Add(1)
		go func(filePath string, m3uIndex int) {
			defer wg.Done()

			streamInfo, err := parseM3UFile(filePath, m3uIndex)
			if err != nil {
				// Handle error appropriately, e.g., log it
				log.Println(fmt.Errorf("parseM3UFile error: %v", err))
				return
			}

			mutex.Lock()
			defer mutex.Unlock()

			// Check if the NewStreams slice is empty, if so, assign the NewStreams directly
			if len(NewStreams) == 0 {
				NewStreams = streamInfo
			} else {
				fmt.Printf("Merging: %s... This will probably take a while...\n", filePath)
				NewStreams = mergeStreamInfo(NewStreams, streamInfo)
			}
		}(path, index)
	}

	wg.Wait()

	Streams = NewStreams

	fmt.Print("Saving to database...\n")
	_ = database.SaveToSQLite(Streams)

	return nil
}

// mergeStreamInfo merges two slices of database.StreamInfo based on Title.
func mergeStreamInfo(existing, new []database.StreamInfo) []database.StreamInfo {
	var wg sync.WaitGroup
	var mutex sync.Mutex

	for _, stream := range new {
		wg.Add(1)
		go func(s database.StreamInfo) {
			defer wg.Done()
			mutex.Lock()
			defer mutex.Unlock()
			found := false
			for i, existingStream := range existing {
				if s.Title == existingStream.Title {
					existing[i].URLs = append(existing[i].URLs, s.URLs...)
					found = true
					break
				}
			}
			if !found {
				existing = append(existing, s)
			}
		}(stream)
	}

	wg.Wait()
	return existing
}

func parseM3UFile(filePath string, m3uIndex int) ([]database.StreamInfo, error) {
	fmt.Printf("Parsing: %s\n", filePath)
	var streams []database.StreamInfo

	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("Open error: %v", err)
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
				key := match[1]
				value := match[2]

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
			// Extract URL
			currentStream.URLs = []database.StreamURL{
				{
					Content:  line,
					M3UIndex: m3uIndex,
				},
			}
			streams = append(streams, currentStream)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanner error: %v", err)
	}

	return streams, nil
}
