package sourceproc

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"m3u-stream-merger/utils"
)

// SourceProcessorResult represents a streaming download result
type SourceProcessorResult struct {
	Index string
	Lines chan *LineDetails
	Error chan error
}

type LineDetails struct {
	Content string
	LineNum int
}

func streamDownloadM3USources() chan *SourceProcessorResult {
	resultChan := make(chan *SourceProcessorResult)
	indexes := utils.GetM3UIndexes()

	go func() {
		defer close(resultChan)
		var wg sync.WaitGroup

		for _, index := range indexes {
			wg.Add(1)
			go func(idx string) {
				defer wg.Done()

				// Create streaming result with buffered channels
				result := &SourceProcessorResult{
					Index: idx,
					Lines: make(chan *LineDetails, 1000),
					Error: make(chan error, 1),
				}

				// Start the streaming process
				go func() {
					defer close(result.Lines)
					defer close(result.Error)

					m3uURL := os.Getenv(fmt.Sprintf("M3U_URL_%s", idx))
					if m3uURL == "" {
						result.Error <- fmt.Errorf("no URL configured for M3U index %s", idx)
						return
					}

					// Handle local files
					if strings.HasPrefix(m3uURL, "file://") {
						handleLocalFileSourceProcessor(strings.TrimPrefix(m3uURL, "file://"), result)
						return
					}

					// Handle remote URLs
					handleRemoteURLSourceProcessor(m3uURL, idx, result)
				}()

				resultChan <- result
			}(index)
		}

		wg.Wait()
	}()

	return resultChan
}

func handleLocalFileSourceProcessor(localPath string, result *SourceProcessorResult) {
	file, err := os.Open(localPath)
	if err != nil {
		result.Error <- fmt.Errorf("error opening local file: %v", err)
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	lineNum := 0
	for scanner.Scan() {
		result.Lines <- &LineDetails{
			Content: scanner.Text(),
			LineNum: lineNum,
		}
		lineNum++
	}

	if err := scanner.Err(); err != nil {
		result.Error <- fmt.Errorf("error reading local file: %v", err)
	}
}

func handleRemoteURLSourceProcessor(m3uURL, idx string, result *SourceProcessorResult) {
	// Create request
	req, err := http.NewRequest("GET", m3uURL, nil)
	if err != nil {
		result.Error <- fmt.Errorf("error creating request: %v", err)
		return
	}

	// Perform request
	resp, err := utils.HTTPClient.Do(req)
	if err != nil {
		result.Error <- fmt.Errorf("HTTP GET error: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		result.Error <- fmt.Errorf("unexpected status code: %d", resp.StatusCode)
		return
	}

	// Create the final file in parallel
	finalPath := utils.GetM3UFilePathByIndex(idx)
	tmpPath := finalPath + ".new"

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(finalPath), os.ModePerm); err != nil {
		result.Error <- fmt.Errorf("error creating directories: %v", err)
		return
	}

	// Create file for writing
	file, err := os.Create(tmpPath)
	if err != nil {
		result.Error <- fmt.Errorf("error creating file: %v", err)
		return
	}

	// Create a TeeReader to write to file while we read
	reader := io.TeeReader(resp.Body, file)

	// Stream the content line by line
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	lineNum := 0
	for scanner.Scan() {
		result.Lines <- &LineDetails{
			Content: scanner.Text(),
			LineNum: lineNum,
		}
		lineNum++
	}

	if err := scanner.Err(); err != nil {
		result.Error <- fmt.Errorf("error reading remote content: %v", err)
		file.Close()
		os.Remove(tmpPath)
		return
	}

	// Close file and move to final location
	file.Close()
	_ = os.Remove(finalPath)
	if err := os.Rename(tmpPath, finalPath); err != nil {
		result.Error <- fmt.Errorf("error moving file to final location: %v", err)
		os.Remove(tmpPath)
		return
	}
}
