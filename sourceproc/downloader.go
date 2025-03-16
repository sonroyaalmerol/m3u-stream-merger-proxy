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

	// Open the existing final file (if any) as a fallback.
	fallbackFile, _ := os.Open(finalPath)
	// Ensure fallbackFile is closed if it remains unused.
	defer func() {
		if fallbackFile != nil {
			fallbackFile.Close()
		}
	}()

	var file *os.File
	var reader io.Reader

	req, err := http.NewRequest("GET", m3uURL, nil)
	if err == nil {
		resp, err := utils.HTTPClient.Do(req)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				bufReader := bufio.NewReader(resp.Body)
				peekBytes, err := bufReader.Peek(7)
				if err != nil {
					logger.Default.Warnf("Error peeking into response: %v", err)
					// Fallback if available.
					if fallbackFile != nil {
						file = fallbackFile
						reader = file
					} else {
						result.Error <- fmt.Errorf("error peeking into response: %v", err)
						return
					}
				}
				if !strings.HasPrefix(string(peekBytes), "#EXTM3U") {
					logger.Default.Warnf(
						"Response for index %s does not appear to be a valid m3u file. Falling "+
							"back to existing file: %s",
						idx, finalPath)
					if fallbackFile != nil {
						file = fallbackFile
						reader = file
					} else {
						result.Error <- fmt.Errorf("response does not appear to be a valid m3u file and no fallback")
						return
					}
				} else {
					// Create a new file
					newFile, err := os.Create(tmpPath)
					if err != nil {
						logger.Default.Warnf("Error creating tmp file for index %s: %v. Falling back to existing file: %s",
							idx, err, finalPath)
						if fallbackFile != nil {
							file = fallbackFile
							reader = file
						} else {
							result.Error <- fmt.Errorf("error creating tmp file: %v", err)
							return
						}
					} else {
						// We don't need the fallback file if we get the new file.
						if fallbackFile != nil {
							fallbackFile.Close()
							fallbackFile = nil
						}
						file = newFile
						// Use the buffered reader (which has been peeked) as the source.
						reader = io.TeeReader(bufReader, file)
					}
				}
			} else {
				logger.Default.Warnf("HTTP status %d for index %s. Falling back to existing file: %s",
					resp.StatusCode, idx, finalPath)
				if fallbackFile != nil {
					file = fallbackFile
					reader = file
				} else {
					result.Error <- fmt.Errorf("HTTP status %d and no existing file", resp.StatusCode)
					return
				}
			}
		} else {
			logger.Default.Warnf("HTTP request error for index %s: %v. Falling back to existing file: %s",
				idx, err, finalPath)
			if fallbackFile != nil {
				file = fallbackFile
				reader = file
			} else {
				result.Error <- fmt.Errorf("HTTP request error: %v", err)
				return
			}
		}
	} else {
		logger.Default.Warnf("Error creating HTTP request for index %s: %v. Falling back to existing file: %s",
			idx, err, finalPath)
		if fallbackFile != nil {
			file = fallbackFile
			reader = file
		} else {
			result.Error <- fmt.Errorf("error creating HTTP request: %v", err)
			return
		}
	}

	scanAndStream(reader, result)

	file.Close()
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
