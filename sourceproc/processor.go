package sourceproc

import (
	"bufio"
	"context"
	"fmt"
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
	storeWriter           *StreamStoreWriter
	criticalErrorOccurred atomic.Bool
	tvgIDs                map[string]struct{}
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
		batch := min(max(int(math.Pow(10, math.Floor(math.Log10(float64(processCount))))), 100), 10000)
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
		p.saveTvgIDs()
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
		LockSources()
		defer UnlockSources()

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

	streamCh := make(chan pendingStream, 4096)
	errors := make(chan error, 4096)

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

		go func() {
			wgProducers.Wait()
			close(streamCh)
		}()

		numWorkers := runtime.NumCPU() * 2
		var wgWorkers sync.WaitGroup
		wgWorkers.Add(numWorkers)

		for range numWorkers {
			go func() {
				defer wgWorkers.Done()
				var slab streamSlab
				for ps := range streamCh {
					stream := slab.parseLine(ps.extinf, &ps.urlLine, ps.m3uIndex)
					if stream == nil || !checkFilter(stream) {
						continue
					}
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

		wgWorkers.Wait()

		p.compileM3U(baseURL)
	}()

	return errors
}

func (p *M3UProcessor) applyNewRemoteFiles() {
	LockSources()
	defer UnlockSources()

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

	if p.storeWriter != nil {
		if err := p.storeWriter.Commit(); err != nil {
			logger.Default.Errorf("Error committing stream store: %v", err)
		}
		p.storeWriter = nil
		cleanupLegacyStores()
	}
}

func (p *M3UProcessor) cleanFailedRemoteFiles() {
	indexes := utils.GetM3UIndexes()
	for _, idx := range indexes {
		finalPath := utils.GetM3UFilePathByIndex(idx)
		tmpPath := finalPath + ".new"
		_ = os.RemoveAll(tmpPath)
	}
	if p.storeWriter != nil {
		p.storeWriter.Discard()
		p.storeWriter = nil
	}
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

	defer func() {
		p.file.Close()
		p.sortingMgr.Close()
		close(p.revalidatingDone)
	}()

	header := "#EXTM3U"
	if len(utils.GetEPGIndexes()) > 0 {
		header += fmt.Sprintf(` url-tvg="%s/epg.xml"`, baseURL)
	}
	header += "\n"
	_, err := p.writer.WriteString(header)
	if err != nil {
		p.markCriticalError(err)
		return
	}

	storeWriter, err := NewStreamStoreWriter()
	if err != nil {
		p.markCriticalError(err)
		return
	}
	p.storeWriter = storeWriter

	p.tvgIDs = make(map[string]struct{})
	err = p.sortingMgr.GetSortedEntries(func(entry *StreamInfo) {
		key, sum := slugParts(entry.Title)
		_, writeErr := p.writer.WriteString(formatStreamEntry(baseURL, sum, entry))
		if writeErr != nil {
			p.markCriticalError(writeErr)
		}
		if storeErr := storeWriter.Add(key, entry); storeErr != nil {
			p.markCriticalError(storeErr)
		}
		if entry.TvgID != "" {
			p.tvgIDs[entry.TvgID] = struct{}{}
		}
	})
	if err != nil {
		p.markCriticalError(err)
		return
	}

	if flushErr := p.writer.Flush(); flushErr != nil {
		p.markCriticalError(flushErr)
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

// pendingStream carries one raw EXTINF/URL pair from the producer to the worker pool.
type pendingStream struct {
	extinf   string
	urlLine  LineDetails
	m3uIndex string
}

func (p *M3UProcessor) handleDownloaded(result *SourceDownloaderResult, streamCh chan<- pendingStream) {
	var currentLine string

	go func() {
		for err := range result.Error {
			if err != nil {
				logger.Default.Errorf("Error processing M3U %s: %v", result.Index, err)
			}
		}
	}()

	for lineInfo := range result.Lines {
		line := strings.TrimSpace(lineInfo.Content)
		if strings.HasPrefix(line, "#EXTINF:") {
			currentLine = line
		} else if currentLine != "" && !strings.HasPrefix(line, "#") {
			streamCh <- pendingStream{extinf: currentLine, urlLine: *lineInfo, m3uIndex: result.Index}
			currentLine = ""
		}
	}
}

// saveTvgIDs writes the collected tvg-id set to disk so the EPG processor can
// filter out channels and programmes not present in the merged playlist.
func (p *M3UProcessor) saveTvgIDs() {
	if len(p.tvgIDs) == 0 {
		return
	}
	if err := os.MkdirAll(config.GetEPGDirPath(), 0755); err != nil {
		logger.Default.Warnf("saveTvgIDs: mkdir: %v", err)
		return
	}
	var sb strings.Builder
	for id := range p.tvgIDs {
		sb.WriteString(id)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(config.GetEPGTvgIDsPath(), []byte(sb.String()), 0644); err != nil {
		logger.Default.Warnf("saveTvgIDs: write: %v", err)
	}
}

// GetTvgIDs returns the set of tvg-id values seen in the last successful
// M3U compilation.  Returns nil when no compilation has run yet.
func (p *M3UProcessor) GetTvgIDs() map[string]struct{} {
	return p.tvgIDs
}

func createResultFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
}
