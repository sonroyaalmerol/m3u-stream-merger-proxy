package sourceproc

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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
	sorter                *spillSorter
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
		sorter:           newSpillSorter(),
	}

	return processor
}

func (p *M3UProcessor) Start(r *http.Request) {
	start := time.Now()
	errors := p.processStreams(r)

	for err := range errors {
		if err != nil {
			logger.Default.Errorf("Error while processing stream: %v", err)
		}
	}

	total := p.streamCount.Load()
	elapsed := time.Since(start).Seconds()
	logger.Default.Logf("Ingest complete: %d streams in %.1fs (%.0f streams/s)", total, elapsed, float64(total)/max(elapsed, 0.001))
}

func (p *M3UProcessor) Wait(ctx context.Context) error {
	select {
	case <-p.revalidatingDone:
	case <-ctx.Done():
		p.Lock()
		defer p.Unlock()

		logger.Default.Errorf("Revalidation failed due to context cancellation, keeping old data.")
		os.Remove(p.file.Name())
		p.cleanFailedRemoteFiles()

		return ctx.Err()
	}

	p.Lock()
	defer p.Unlock()

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
	p.RLock()
	defer p.RUnlock()

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

	tracker := newIngestProgress(p.streamCount.Load)
	results := streamDownloadM3USources(tracker)
	baseURL := utils.DetermineBaseURL(r)

	streamCh := make(chan pendingStream, 8192)
	errors := make(chan error, 4096)

	go func() {
		defer close(errors)
		defer p.cleanup()

		go tracker.run()

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
					if err == nil {
						continue
					}
					p.markCriticalError(err)

					select {
					case errors <- err:
					default:
						logger.Default.Errorf("Error channel full, dropping error: %v", err)
					}
				}
			}()
		}

		wgWorkers.Wait()
		tracker.stop()

		logger.Default.Logf("Parsing complete: %d streams accepted, compiling playlist", p.streamCount.Load())
		p.compileM3U(baseURL)
	}()

	return errors
}

// compileM3U folds, sorts, and renders via the spill sorter in one ordered write pass.
func (p *M3UProcessor) compileM3U(baseURL string) {
	p.Lock()
	defer p.Unlock()

	defer func() {
		p.file.Close()
		p.sorter.Close()
		close(p.revalidatingDone)
	}()

	header := "#EXTM3U"
	if len(utils.GetEPGIndexes()) > 0 {
		header += fmt.Sprintf(` url-tvg="%s/epg.xml"`, baseURL)
	}
	header += "\n"
	if _, err := p.writer.WriteString(header); err != nil {
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

	render := func(entry *StreamInfo, rb *renderBuf) renderedEntry {
		rb.m3u.Reset()
		rb.rec.Reset()

		key, sum := slugParts(entry.Title)
		if err := writeStreamEntry(&rb.m3u, baseURL, sum, entry, rb.slug); err != nil {
			p.markCriticalError(err)
		}
		if err := rb.enc.Encode(entry); err != nil {
			p.markCriticalError(err)
			rb.rec.Reset()
		}
		rb.tvg = append(rb.tvg[:0], entry.TvgID...)

		return renderedEntry{storeKey: key, m3u: rb.m3u.Bytes(), storeRec: rb.rec.Bytes(), tvgID: rb.tvg}
	}
	emit := func(re renderedEntry) error {
		if _, err := p.writer.Write(re.m3u); err != nil {
			return err
		}
		if len(re.storeRec) > 0 {
			if err := storeWriter.AddRaw(re.storeKey, re.storeRec); err != nil {
				return err
			}
		}
		if len(re.tvgID) > 0 {
			if _, ok := p.tvgIDs[string(re.tvgID)]; !ok {
				p.tvgIDs[string(re.tvgID)] = struct{}{}
			}
		}
		return nil
	}

	if err := p.sorter.MergeRendered(render, emit); err != nil {
		p.markCriticalError(err)
		return
	}
	if err := p.writer.Flush(); err != nil {
		p.markCriticalError(err)
	}
}

func (p *M3UProcessor) applyNewRemoteFiles() {
	LockSources()
	defer UnlockSources()

	indexes := utils.GetM3UIndexes()

	for _, idx := range indexes {
		finalPath := utils.GetM3UFilePathByIndex(idx)
		tmpPath := finalPath + ".new"
		if _, err := os.Stat(tmpPath); err == nil {
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

	return p.sorter.Add(stream)
}

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

// saveTvgIDs persists the tvg-id set so the EPG processor can filter to merged channels.
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

func (p *M3UProcessor) cleanup() {
	p.Lock()
	defer p.Unlock()

	if p.writer != nil {
		p.writer.Flush()
	}
	if p.file != nil {
		p.file.Close()
	}
}

func createResultFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
}
