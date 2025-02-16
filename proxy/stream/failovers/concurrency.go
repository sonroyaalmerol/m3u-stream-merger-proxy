package failovers

import (
	"context"
	"m3u-stream-merger/store"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type SegmentCallDetector struct {
	mu              sync.Mutex
	lastSegmentCall time.Time
	timeout         time.Duration

	activeConcurrency atomic.Bool

	concurrencyMgr *store.ConcurrencyManager
	m3uIndex       string
}

func NewSegmentCallDetector(ctx context.Context,
	concurrencyMgr *store.ConcurrencyManager, m3uIndex string,
	timeout time.Duration) *SegmentCallDetector {

	detector := &SegmentCallDetector{
		lastSegmentCall: time.Now(),
		timeout:         timeout,
		concurrencyMgr:  concurrencyMgr,
		m3uIndex:        m3uIndex,
	}

	go detector.monitor(ctx)
	return detector
}

func (pd *SegmentCallDetector) monitor(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			pd.updateConcurrencyState()
			return
		case <-ticker.C:
			pd.updateConcurrencyState()
		}
	}
}

func (pd *SegmentCallDetector) updateConcurrencyState() {
	pd.mu.Lock()
	active := time.Since(pd.lastSegmentCall) <= pd.timeout
	pd.mu.Unlock()

	index := strings.Split(pd.m3uIndex, "|")[0]

	if active {
		if pd.activeConcurrency.CompareAndSwap(false, true) {
			pd.concurrencyMgr.UpdateConcurrency(index, true)
		}
	} else {
		if pd.activeConcurrency.CompareAndSwap(true, false) {
			pd.concurrencyMgr.UpdateConcurrency(index, false)
		}
	}
}

func (pd *SegmentCallDetector) SegmentCall() {
	pd.mu.Lock()
	pd.lastSegmentCall = time.Now()
	pd.mu.Unlock()
}

func (pd *SegmentCallDetector) IsActive() bool {
	pd.mu.Lock()
	defer pd.mu.Unlock()
	return time.Since(pd.lastSegmentCall) <= pd.timeout
}
