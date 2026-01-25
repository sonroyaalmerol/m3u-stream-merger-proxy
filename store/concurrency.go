package store

import (
	"fmt"
	"m3u-stream-merger/logger"
	"os"
	"strconv"
	"sync/atomic"

	"github.com/puzpuzpuz/xsync/v3"
)

type ConcurrencyManager struct {
	count    *xsync.MapOf[string, *atomic.Int32]
	Invalid  *xsync.MapOf[string, struct{}]
	maxCache *xsync.MapOf[string, int]
}

func NewConcurrencyManager() *ConcurrencyManager {
	return &ConcurrencyManager{
		count:    xsync.NewMapOf[string, *atomic.Int32](),
		Invalid:  xsync.NewMapOf[string, struct{}](),
		maxCache: xsync.NewMapOf[string, int](),
	}
}

func (cm *ConcurrencyManager) getMaxConcurrency(m3uIndex string) int {
	if max, ok := cm.maxCache.Load(m3uIndex); ok {
		return max
	}

	envVar := fmt.Sprintf("M3U_MAX_CONCURRENCY_%s", m3uIndex)
	maxStr := os.Getenv(envVar)
	max, err := strconv.Atoi(maxStr)
	if err != nil || max < 1 {
		max = 1
	}

	cm.maxCache.Store(m3uIndex, max)
	return max
}

func (cm *ConcurrencyManager) GetConcurrencyStatus(m3uIndex string) (current int, max int, priority int) {
	max = cm.getMaxConcurrency(m3uIndex)
	if val, ok := cm.count.Load(m3uIndex); ok {
		current = int(val.Load())
	}
	priority = max - current
	return
}

func (cm *ConcurrencyManager) CheckConcurrency(m3uIndex string) bool {
	max := cm.getMaxConcurrency(m3uIndex)
	if val, ok := cm.count.Load(m3uIndex); ok {
		current := int(val.Load())
		logger.Default.Logf("Current connections for M3U_%s: %d/%d", m3uIndex, current, max)
		return current >= max
	}
	logger.Default.Logf("Current connections for M3U_%s: 0/%d", m3uIndex, max)
	return false
}

func (cm *ConcurrencyManager) ConcurrencyPriorityValue(m3uIndex string) int {
	_, _, priority := cm.GetConcurrencyStatus(m3uIndex)
	return priority
}

func (cm *ConcurrencyManager) UpdateConcurrency(m3uIndex string, incr bool) bool {
	max := cm.getMaxConcurrency(m3uIndex)
	var finalCount int32
	var success bool

	cm.count.Compute(m3uIndex, func(count *atomic.Int32, _ bool) (*atomic.Int32, bool) {
		if count == nil {
			count = &atomic.Int32{}
		}

		if incr {
			current := count.Load()
			if current < int32(max) {
				count.Add(1)
				success = true
			} else {
				success = false
			}
		} else {
			if count.Load() > 0 {
				count.Add(-1)
			}
			success = true
		}

		finalCount = count.Load()
		return count, false
	})

	logger.Default.Logf("Updated connections for M3U_%s: %d/%d (success=%v)",
		m3uIndex, finalCount, max, success)
	return success
}

func (cm *ConcurrencyManager) GetCount(m3uIndex string) int {
	if val, ok := cm.count.Load(m3uIndex); ok {
		return int(val.Load())
	}
	return 0
}
