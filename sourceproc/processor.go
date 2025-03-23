package sourceproc

import (
	"bufio"
	"context"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"m3u-stream-merger/config"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/utils"
)

type M3UProcessor struct {
	sync.RWMutex
	streamCount           atomic.Int64
	file                  *os.File
	writer                *bufio.Writer
	revalidatingDone      chan struct{}
	sortingMgr            *SortingManager
	criticalErrorOccurred atomic.Bool
}

func NewProcessor() *M3UProcessor {
	processedPath := config.GetNewM3UPath() + ".tmp"
	file, err := createResultFile(processedPath)
	if err != nil {
		logger.Default.Errorf("Error creating result file: %v", err)
		return nil
	}

	processor := &M3UProcessor{
		file:             file,
		writer:           bufio.NewWriter(file),
		revalidatingDone: make(chan struct{}),
		sortingMgr:       newSortingManager(),
	}

	return processor
}

func (p *M3UProcessor) Start(r *http.Request) {
	processCount := 0
	errors := p.processStreams(r)
	for err := range errors {
		if err != nil {
			logger.Default.Errorf("Error while processing stream: %v", err)
		}
		processCount++
		batch := int(math.Pow(10, math.Floor(math.Log10(float64(processCount)))))
		if batch < 100 {
			batch = 100
		}
		if processCount%batch == 0 {
			logger.Default.Logf("Processed %d streams so far", processCount)
		}
	}
	logger.Default.Logf("Completed processing %d total streams", processCount)
}

func (p *M3UProcessor) Wait(ctx context.Context) error {
	select {
	case <-p.revalidatingDone:
	case <-ctx.Done():
		logger.Default.Errorf("Revalidation failed due to context cancellation, keeping old data.")
		os.Remove(p.file.Name())
		p.cleanFailedRemoteFiles()

		return ctx.Err()
	}
	logger.Default.Debug("Finished revalidation")

	if !p.criticalErrorOccurred.Load() {
		logger.Default.Debug("Error has not occurred")
		prodPath := strings.TrimSuffix(p.file.Name(), ".tmp")

		logger.Default.Debugf("Renaming %s to %s", p.file.Name(), prodPath)
		err := os.Rename(p.file.Name(), prodPath)
		if err != nil {
			logger.Default.Errorf("Error renaming file: %v", err)
		}
		p.applyNewRemoteFiles()
		p.clearOldResults()
	} else {
		logger.Default.Errorf("Revalidation failed, keeping old data.")
		os.Remove(p.file.Name())
		p.file = nil
		p.cleanFailedRemoteFiles()
	}

	return nil
}

func (p *M3UProcessor) Run(ctx context.Context, r *http.Request) error {
	p.Start(r)
	return p.Wait(ctx)
}

func (p *M3UProcessor) GetCount() int {
	return int(p.streamCount.Load())
}

func (p *M3UProcessor) clearOldResults() {
	prodPath := strings.TrimSuffix(p.file.Name(), ".tmp")
	err := config.ClearOldProcessedM3U(prodPath)
	if err != nil {
		logger.Default.Error(err.Error())
	}
}

func (p *M3UProcessor) GetResultPath() string {
	if p.file == nil {
		path, err := config.GetLatestProcessedM3UPath()
		if err != nil {
			return ""
		}
		return path
	}
	prodPath := strings.TrimSuffix(p.file.Name(), ".tmp")
	return prodPath
}

func (p *M3UProcessor) markCriticalError(err error) {
	logger.Default.Errorf("Critical error during source processing: %v", err)
	p.criticalErrorOccurred.Store(true)
}

func (p *M3UProcessor) processStreams(r *http.Request) chan error {
	revalidating := true
	select {
	case _, revalidating = <-p.revalidatingDone:
	default:
	}

	if !revalidating {
		p.revalidatingDone = make(chan struct{})
	}

	results := streamDownloadM3USources()
	baseURL := utils.DetermineBaseURL(r)

	// Increase channel buffer sizes
	errors := make(chan error, 1000)         // Increased error buffer
	streamCh := make(chan *StreamInfo, 1000) // Larger stream buffer

	go func() {
		defer close(errors)
		defer p.cleanup()

		var wgProducers sync.WaitGroup
		for result := range results {
			wgProducers.Add(1)
			go func(res *SourceDownloaderResult) {
				defer wgProducers.Done()
				p.handleDownloaded(res, streamCh)
			}(result)
		}

		// Close streamCh after all producers finish
		go func() {
			wgProducers.Wait()
			close(streamCh)
		}()

		// Worker pool to process streams concurrently
		numWorkers := runtime.NumCPU() * 2
		var wgWorkers sync.WaitGroup
		wgWorkers.Add(numWorkers)

		for i := 0; i < numWorkers; i++ {
			go func() {
				defer wgWorkers.Done()
				for stream := range streamCh {
					err := p.addStream(stream)
					if err != nil {
						p.markCriticalError(err)
					}

					select {
					case errors <- err:
					default:
						logger.Default.Errorf("Error channel full, dropping error: %v", err)
					}
				}
			}()
		}

		wgWorkers.Wait() // Wait for all streams to be processed

		p.compileM3U(baseURL)
	}()

	return errors
}

func (p *M3UProcessor) applyNewRemoteFiles() {
	indexes := utils.GetM3UIndexes()
	for _, idx := range indexes {
		finalPath := utils.GetM3UFilePathByIndex(idx)
		tmpPath := finalPath + ".new"
		if _, err := os.Stat(tmpPath); err == nil {
			// Rename the temporary file to the final file.
			if err := os.Rename(tmpPath, finalPath); err != nil {
				logger.Default.Errorf("Error renaming remote file %s: %v", tmpPath, err)
			}
		}
	}
}

func (p *M3UProcessor) cleanFailedRemoteFiles() {
	indexes := utils.GetM3UIndexes()
	for _, idx := range indexes {
		finalPath := utils.GetM3UFilePathByIndex(idx)
		tmpPath := finalPath + ".new"
		_ = os.RemoveAll(tmpPath)
	}
}

func (p *M3UProcessor) addStream(stream *StreamInfo) error {
	if stream == nil || stream.URLs.Size() == 0 {
		return nil
	}

	p.streamCount.Add(1)

	return p.sortingMgr.AddToSorter(stream)
}

func (p *M3UProcessor) compileM3U(baseURL string) {
	p.Lock()
	defer p.Unlock()

	defer func() {
		p.file.Close()
		p.sortingMgr.Close()
		close(p.revalidatingDone)
	}()

	_, err := p.writer.WriteString("#EXTM3U\n")
	if err != nil {
		p.markCriticalError(err)
		return
	}

	err = p.sortingMgr.GetSortedEntries(func(entry *StreamInfo) {
		_, writeErr := p.writer.WriteString(formatStreamEntry(baseURL, entry))
		if writeErr != nil {
			p.markCriticalError(err)
		}
	})
	if err != nil {
		p.markCriticalError(err)
		return
	}

	if flushErr := p.writer.Flush(); flushErr != nil {
		p.markCriticalError(err)
		return
	}
}

func (p *M3UProcessor) cleanup() {
	if p.writer != nil {
		p.writer.Flush()
	}
	if p.file != nil {
		p.file.Close()
	}
}

func (p *M3UProcessor) handleDownloaded(result *SourceDownloaderResult, streamCh chan<- *StreamInfo) {
	var currentLine string

	// Handle errors asynchronously
	go func() {
		for err := range result.Error {
			if err != nil {
				logger.Default.Errorf("Error processing M3U %s: %v", result.Index, err)
			}
		}
	}()

	// Process lines as they come in
	for lineInfo := range result.Lines {
		line := strings.TrimSpace(lineInfo.Content)
		if strings.HasPrefix(line, "#EXTINF:") {
			currentLine = line
		} else if currentLine != "" && !strings.HasPrefix(line, "#") {
			if streamInfo := parseLine(currentLine, lineInfo, result.Index); streamInfo != nil {
				if checkFilter(streamInfo) {
					streamCh <- streamInfo
				}
			}
			currentLine = ""
		}
	}
}

func createResultFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
}
