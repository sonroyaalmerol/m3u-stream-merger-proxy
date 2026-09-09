package sourceproc

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"m3u-stream-merger/logger"
)

const (
	progressInterval  = 15 * time.Second
	progressHeartbeat = time.Minute
)

// sourceProgress is one source's live line counter plus optional phase detail.
type sourceProgress struct {
	idx   string
	kind  string
	lines atomic.Int64
}

// ingestProgress collapses all sources + the parser into one heartbeat line,
// logged only on change, with a slow liveness floor.
type ingestProgress struct {
	start   time.Time
	streams func() int64

	mu      sync.Mutex
	sources []*sourceProgress

	done chan struct{}
}

func newIngestProgress(streams func() int64) *ingestProgress {
	return &ingestProgress{
		start:   time.Now(),
		streams: streams,
		done:    make(chan struct{}),
	}
}

func (p *ingestProgress) register(idx, kind string) *sourceProgress {
	sp := &sourceProgress{idx: idx, kind: kind}
	p.mu.Lock()
	p.sources = append(p.sources, sp)
	p.mu.Unlock()
	return sp
}

func (p *ingestProgress) run() {
	ticker := time.NewTicker(progressInterval)
	defer ticker.Stop()

	var lastLines, lastStreams int64
	lastLog := time.Now()
	for {
		select {
		case <-p.done:
			return
		case <-ticker.C:
			lines, streams := p.totals()
			if lines == lastLines && streams == lastStreams && time.Since(lastLog) < progressHeartbeat {
				continue
			}
			lastLines, lastStreams, lastLog = lines, streams, time.Now()
			logger.Default.Logf("Ingest: %s -> %d streams (%.0fs)",
				p.describe(lines), streams, time.Since(p.start).Seconds())
		}
	}
}

func (p *ingestProgress) stop() {
	select {
	case <-p.done:
	default:
		close(p.done)
	}
}

func (p *ingestProgress) totals() (lines, streams int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.sources {
		lines += s.lines.Load()
	}
	return lines, p.streams()
}

func (p *ingestProgress) describe(total int64) string {
	p.mu.Lock()
	srcs := append([]*sourceProgress(nil), p.sources...)
	p.mu.Unlock()

	parts := make([]string, 0, len(srcs))
	for _, s := range srcs {
		parts = append(parts, fmt.Sprintf("%s=%d", s.idx, s.lines.Load()))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%d lines", total)
	}
	return fmt.Sprintf("%d lines (%s)", total, strings.Join(parts, ", "))
}
