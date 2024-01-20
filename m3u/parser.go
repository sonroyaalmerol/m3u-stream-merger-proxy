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
	err := loadM3UFiles(skipClearing)
	if err != nil {
		return err
	}

	var wg sync.WaitGroup
	var mutex sync.Mutex

	for _, path := range m3uFilePaths {
		wg.Add(1)
		go func(filePath string) {
			defer wg.Done()

			streamInfo, err := parseM3UFile(filePath)
			if err != nil {
				// Handle error appropriately, e.g., log it
				log.Println(err.Error())
				return
			}

			mutex.Lock()
			defer mutex.Unlock()

			Streams = mergeStreamInfo(Streams, streamInfo)
		}(path)
	}

	wg.Wait()

	return nil
}

// mergeStreamInfo merges two slices of StreamInfo based on Title.
func mergeStreamInfo(existing, new []StreamInfo) []StreamInfo {
	for _, stream := range new {
		fmt.Printf("Processing: %s\n", stream.Title)
		found := false
		for i, existingStream := range existing {
			if stream.Title == existingStream.Title {
				fmt.Printf("Merging: %s\n", existingStream.Title)
				existing[i].URLs = append(existing[i].URLs, stream.URLs...)
				found = true
				break
			}
		}
		if !found {
			existing = append(existing, stream)
		}
	}
	return existing
}

func parseM3UFile(filePath string) ([]StreamInfo, error) {
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
			parts := strings.SplitN(line, ",", 2)
			if len(parts) == 2 {
				currentStream.Title = parts[1]
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
					Content: line,
					Used:    false,
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
