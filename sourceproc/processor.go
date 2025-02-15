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
	streamCount      atomic.Int64
	file             *os.File
	writer           *bufio.Writer
	revalidatingDone chan struct{}
	sortingMgr       *SortingManager
}

func NewProcessor() *M3UProcessor {
	processedPath := config.GetNewM3UPath()
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
		return ctx.Err()
	}

	p.clearOldResults()

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
	err := config.ClearOldProcessedM3U(p.file.Name())
	if err != nil {
		logger.Default.Error(err.Error())
	}
}

func (p *M3UProcessor) GetResultPath() string {
	if p.file == nil {
		return ""
	}
	return p.file.Name()
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
		numWorkers := runtime.NumCPU()
		var wgWorkers sync.WaitGroup
		wgWorkers.Add(numWorkers)

		for i := 0; i < numWorkers; i++ {
			go func() {
				defer wgWorkers.Done()
				for stream := range streamCh {
					if err := p.addStream(stream); err != nil {
						select {
						case errors <- err:
						default:
							logger.Default.Errorf("Error channel full, dropping error: %v", err)
						}
					}
				}
			}()
		}

		wgWorkers.Wait() // Wait for all streams to be processed

		p.compileM3U(baseURL)
	}()

	return errors
}

func (p *M3UProcessor) addStream(stream *StreamInfo) error {
	if stream == nil || len(stream.URLs) == 0 {
		return nil
	}

	p.streamCount.Add(1)

	return p.sortingMgr.AddToSorter(stream)
}

func (p *M3UProcessor) compileM3U(baseURL string) {
	p.Lock()
	defer p.Unlock()

	_, err := p.writer.WriteString("#EXTM3U\n")
	if err != nil {
		logger.Default.Errorf("Error writing to M3U file: %v", err)
	}

	err = p.sortingMgr.GetSortedEntries(func(entry *StreamInfo) {
		_, writeErr := p.writer.WriteString(formatStreamEntry(baseURL, entry))
		if writeErr != nil {
			logger.Default.Errorf("Error writing to M3U file: %v", err)
		}
	})
	if err != nil {
		logger.Default.Errorf("Error streaming sorted entries: %v", err)
	}

	p.writer.Flush()
	p.file.Close()

	p.sortingMgr.Close()

	close(p.revalidatingDone)
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
