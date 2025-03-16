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

	"m3u-stream-merger/logger"
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
	finalPath := utils.GetM3UFilePathByIndex(idx)
	tmpPath := finalPath + ".new"

	if err := os.MkdirAll(filepath.Dir(finalPath), os.ModePerm); err != nil {
		result.Error <- fmt.Errorf("error creating dir for source: %v", err)
		return
	}

	fallbackFile, _ := os.Open(finalPath)
	defer func() {
		if fallbackFile != nil {
			fallbackFile.Close()
		}
	}()

	useFallback := func(err error) {
		if fallbackFile != nil {
			scanAndStream(fallbackFile, result)
		} else {
			result.Error <- err
		}
	}

	req, err := http.NewRequest("GET", m3uURL, nil)
	if err != nil {
		logger.Default.Warnf("Error creating HTTP request for index %s: %v", idx, err)
		useFallback(fmt.Errorf("error creating HTTP request: %v", err))
		return
	}

	resp, err := utils.HTTPClient.Do(req)
	if err != nil {
		logger.Default.Warnf("HTTP request error for index %s: %v", idx, err)
		useFallback(fmt.Errorf("HTTP request error: %v", err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logger.Default.Warnf("HTTP status %d for index %s", resp.StatusCode, idx)
		useFallback(fmt.Errorf("HTTP status %d and no existing file", resp.StatusCode))
		return
	}

	bufReader := bufio.NewReader(resp.Body)
	peekBytes, err := bufReader.Peek(7)
	if err != nil || !strings.HasPrefix(string(peekBytes), "#EXTM3U") {
		logger.Default.Warnf("Invalid M3U response for index %s. Falling back to existing file: %s", idx, finalPath)
		useFallback(fmt.Errorf("invalid M3U response and no fallback"))
		return
	}

	newFile, err := os.Create(tmpPath)
	if err != nil {
		logger.Default.Warnf("Error creating tmp file for index %s: %v", idx, err)
		useFallback(fmt.Errorf("error creating tmp file: %v", err))
		return
	}
	defer newFile.Close()

	if fallbackFile != nil {
		fallbackFile.Close()
		fallbackFile = nil
	}

	reader := io.TeeReader(bufReader, newFile)
	scanAndStream(reader, result)
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
