package sourceproc

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"m3u-stream-merger/logger"
	"m3u-stream-merger/utils"
	"m3u-stream-merger/xtream"
)

type LineDetails struct {
	Content string
	LineNum int
}

type SourceDownloaderResult struct {
	Index string
	Kind  string
	Lines chan *LineDetails
	Error chan error

	sp *sourceProgress
}

// Count reports lines seen so far for this source.
func (r *SourceDownloaderResult) Count() int64 {
	if r.sp == nil {
		return 0
	}
	return r.sp.lines.Load()
}
func (r *SourceDownloaderResult) addLine() {
	r.sp.lines.Add(1)
}

func (r *SourceDownloaderResult) setDetail(format string, args ...any) {
	if r.sp != nil {
		r.sp.setDetail(format, args...)
	}
}

// streamDownloadM3USources runs one goroutine per source; progress goes through the shared tracker.
func streamDownloadM3USources(tracker *ingestProgress) chan *SourceDownloaderResult {
	resultChan := make(chan *SourceDownloaderResult)
	indexes := utils.GetM3UIndexes()

	go func() {
		defer close(resultChan)
		var wg sync.WaitGroup

		for _, index := range indexes {
			wg.Add(1)
			go func(idx string) {
				defer wg.Done()

				kind := sourceKind(idx)
				result := &SourceDownloaderResult{
					Index: idx,
					Kind:  kind,
					Lines: make(chan *LineDetails, 1000),
					Error: make(chan error, 4),
				}
				result.sp = tracker.register(idx, kind)

				go func() {
					defer close(result.Lines)
					defer close(result.Error)

					m3uURL := os.Getenv(fmt.Sprintf("M3U_URL_%s", idx))
					xtreamURL := os.Getenv(fmt.Sprintf("XTREAM_URL_%s", idx))
					if m3uURL == "" && xtreamURL == "" {
						result.Error <- fmt.Errorf("no URL configured for M3U index %s", idx)
						return
					}

					start := time.Now()
					logger.Default.Logf("Downloading source %s (%s)", idx, kind)

					if m3uURL != "" {
						if after, ok := strings.CutPrefix(m3uURL, "file://"); ok {
							handleLocalFile(after, result)
						} else {
							handleRemoteURL(m3uURL, idx, result)
						}
					} else {
						handleXtreamSource(context.Background(), idx, result)
					}

					elapsed := time.Since(start).Seconds()
					logger.Default.Logf("Downloaded source %s (%s): %d lines in %.1fs", idx, kind, result.Count(), elapsed)
				}()

				resultChan <- result
			}(index)
		}

		wg.Wait()
	}()

	return resultChan
}

func sourceKind(idx string) string {
	m3uURL := os.Getenv(fmt.Sprintf("M3U_URL_%s", idx))
	if m3uURL == "" {
		return "xtream"
	}
	if strings.HasPrefix(m3uURL, "file://") {
		return "file"
	}
	return "m3u"
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

	resp, err := utils.CustomHttpRequest(nil, "GET", m3uURL)
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

func handleXtreamSource(ctx context.Context, idx string, result *SourceDownloaderResult) {
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

	client := xtream.NewClient(
		os.Getenv(fmt.Sprintf("XTREAM_URL_%s", idx)),
		os.Getenv(fmt.Sprintf("XTREAM_USERNAME_%s", idx)),
		os.Getenv(fmt.Sprintf("XTREAM_PASSWORD_%s", idx)),
	)

	newFile, err := os.Create(tmpPath)
	if err != nil {
		useFallback(fmt.Errorf("error creating tmp file for index %s: %v", idx, err))
		return
	}
	defer newFile.Close()

	writer := bufio.NewWriter(newFile)
	if _, err := writer.WriteString("#EXTM3U\n"); err != nil {
		_ = os.Remove(tmpPath)
		useFallback(fmt.Errorf("error writing header for index %s: %v", idx, err))
		return
	}

	lineNum := 0
	emitted := false
	fetchErr := xtream.FetchPlaylistLines(ctx, client, func(line string) error {
		if _, err := writer.WriteString(line + "\n"); err != nil {
			return err
		}
		result.Lines <- &LineDetails{Content: line, LineNum: lineNum}
		result.addLine()
		lineNum++
		emitted = true
		return nil
	}, func(done, total int) {
		result.setDetail("series %d/%d", done, total)
	})

	if fetchErr != nil {
		_ = writer.Flush()
		_ = os.Remove(tmpPath)
		if !emitted {
			logger.Default.Warnf("Xtream fetch error for index %s: %v", idx, fetchErr)
			useFallback(fetchErr)
		} else {
			result.Error <- fmt.Errorf("xtream index %s partial fetch: %w", idx, fetchErr)
		}
		return
	}

	if err := writer.Flush(); err != nil {
		_ = os.Remove(tmpPath)
		useFallback(fmt.Errorf("error flushing tmp file for index %s: %v", idx, err))
		return
	}

	if fallbackFile != nil {
		fallbackFile.Close()
		fallbackFile = nil
	}
}

// ponytail: 32 KiB arena chunks, one pinned per surviving line; shrink if a heavy EXCLUDE filter keeps few lines per chunk.
const (
	lineArenaChunk = 32 << 10
	lineSlabSize   = 512
)

func scanAndStream(r io.Reader, result *SourceDownloaderResult) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var (
		arena []byte
		slab  []LineDetails
	)

	lineNum := 0
	for scanner.Scan() {
		b := scanner.Bytes()
		content := ""
		if len(b) > 0 {
			if len(arena) < len(b) {
				arena = make([]byte, max(lineArenaChunk, len(b)))
			}
			n := copy(arena, b)
			content = unsafe.String(unsafe.SliceData(arena), n)
			arena = arena[n:]
		}

		if len(slab) == 0 {
			slab = make([]LineDetails, lineSlabSize)
		}
		line := &slab[0]
		slab = slab[1:]
		line.Content = content
		line.LineNum = lineNum

		result.Lines <- line
		result.addLine()
		lineNum++
	}

	if err := scanner.Err(); err != nil {
		result.Error <- fmt.Errorf("error reading content: %v", err)
	}
}
