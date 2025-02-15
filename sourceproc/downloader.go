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

type SourceDownloaderResult struct {
	Index string
	Lines chan *LineDetails
	Error chan error
}

type LineDetails struct {
	Content string
	LineNum int
}

func streamDownloadM3USources() chan *SourceDownloaderResult {
	resultChan := make(chan *SourceDownloaderResult)
	indexes := utils.GetM3UIndexes()

	go func() {
		defer close(resultChan)
		var wg sync.WaitGroup

		for _, index := range indexes {
			wg.Add(1)
			go func(idx string) {
				defer wg.Done()

				result := &SourceDownloaderResult{
					Index: idx,
					Lines: make(chan *LineDetails, 1000),
					Error: make(chan error, 1),
				}

				go func() {
					defer close(result.Lines)
					defer close(result.Error)

					m3uURL := os.Getenv(fmt.Sprintf("M3U_URL_%s", idx))
					if m3uURL == "" {
						result.Error <- fmt.Errorf("no URL configured for M3U index %s", idx)
						return
					}

					if strings.HasPrefix(m3uURL, "file://") {
						handleLocalFile(strings.TrimPrefix(m3uURL, "file://"), result)
						return
					}

					handleRemoteURL(m3uURL, idx, result)
				}()

				resultChan <- result
			}(index)
		}

		wg.Wait()
	}()

	return resultChan
}

func handleLocalFile(localPath string, result *SourceDownloaderResult) {
	file, err := os.Open(localPath)
	if err != nil {
		result.Error <- fmt.Errorf("error opening local file: %v", err)
		return
	}
	defer file.Close()

	scanAndStream(file, result)
}

func handleRemoteURL(m3uURL, idx string, result *SourceDownloaderResult) {
	req, err := http.NewRequest("GET", m3uURL, nil)
	if err != nil {
		result.Error <- fmt.Errorf("error creating request: %v", err)
		return
	}

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

	finalPath := utils.GetM3UFilePathByIndex(idx)
	tmpPath := finalPath + ".new"

	if err := os.MkdirAll(filepath.Dir(finalPath), os.ModePerm); err != nil {
		result.Error <- fmt.Errorf("error creating directories: %v", err)
		return
	}

	file, err := os.Create(tmpPath)
	if err != nil {
		result.Error <- fmt.Errorf("error creating file: %v", err)
		return
	}

	reader := io.TeeReader(resp.Body, file)
	scanAndStream(reader, result)

	file.Close()
	_ = os.Remove(finalPath)
	if err := os.Rename(tmpPath, finalPath); err != nil {
		result.Error <- fmt.Errorf("error moving file: %v", err)
		os.Remove(tmpPath)
	}
}

func scanAndStream(r io.Reader, result *SourceDownloaderResult) {
	scanner := bufio.NewScanner(r)
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
		result.Error <- fmt.Errorf("error reading content: %v", err)
	}
}
