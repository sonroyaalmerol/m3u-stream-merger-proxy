package m3u

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
)

// GetStreams retrieves and merges stream information from multiple M3U files.
func GetStreams(skipClearing bool) error {
	if !skipClearing {
		// init
		log.Println("Loading from JSON...")
		fromJson, err := loadFromJSON()
		if err == nil {
			Streams = fromJson

			return nil
		}
	}

	err := loadM3UFiles(skipClearing)
	if err != nil {
		return err
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
				log.Println(err.Error())
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

	fmt.Print("Saving to JSON...\n")
	_ = saveToJSON(Streams)

	return nil
}

// mergeStreamInfo merges two slices of StreamInfo based on Title.
func mergeStreamInfo(existing, new []StreamInfo) []StreamInfo {
	var wg sync.WaitGroup
	var mutex sync.Mutex

	for _, stream := range new {
		wg.Add(1)
		go func(s StreamInfo) {
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

func parseM3UFile(filePath string, m3uIndex int) ([]StreamInfo, error) {
	fmt.Printf("Parsing: %s\n", filePath)
	var streams []StreamInfo

	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)

	var currentStream StreamInfo

	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "#EXTINF:") {
			// Extract stream information from #EXTINF line
			currentStream = StreamInfo{}
			parts := strings.Split(line, " ")
			for _, part := range parts {
				if strings.HasPrefix(part, "tvg-id=") {
					currentStream.TvgID = strings.TrimPrefix(strings.TrimSuffix(part, `"`), `tvg-id="`)
				} else if strings.HasPrefix(part, "tvg-name=") {
					currentStream.Title = strings.TrimPrefix(strings.TrimSuffix(part, `"`), `tvg-name="`)
				} else if strings.HasPrefix(part, "group-title=") {
					currentStream.Group = strings.TrimPrefix(strings.TrimSuffix(part, `"`), `group-title="`)
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
			currentStream.URLs = []StreamURL{
				{
					Content:  line,
					M3UIndex: m3uIndex,
				},
			}
			streams = append(streams, currentStream)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return streams, nil
}
