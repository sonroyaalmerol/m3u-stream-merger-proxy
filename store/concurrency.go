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
	maxCache *xsync.MapOf[string, int] // Cache for parsed max concurrency values
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
	current, max, _ := cm.GetConcurrencyStatus(m3uIndex)
	logger.Default.Logf("Current connections for M3U_%s: %d/%d", m3uIndex, current, max)
	return current >= max
}

func (cm *ConcurrencyManager) ConcurrencyPriorityValue(m3uIndex string) int {
	_, _, priority := cm.GetConcurrencyStatus(m3uIndex)
	return priority
}

func (cm *ConcurrencyManager) UpdateConcurrency(m3uIndex string, incr bool) {
	current, _ := cm.count.Compute(m3uIndex, func(count *atomic.Int32, _ bool) (newValue *atomic.Int32, delete bool) {
		if count == nil {
			count = &atomic.Int32{}
		}
		if incr {
			count.Add(1)
		} else {
			if count.Load() > 0 {
				count.Add(-1)
			}
		}
		return count, false
	})

	max := cm.getMaxConcurrency(m3uIndex)
	logger.Default.Logf("Updated connections for M3U_%s: %d/%d", m3uIndex, current.Load(), max)
}

func (cm *ConcurrencyManager) Increment(m3uIndex string) {
	cm.count.Compute(m3uIndex, func(val *atomic.Int32, loaded bool) (*atomic.Int32, bool) {
		if val == nil {
			val = &atomic.Int32{}
		}
		val.Add(1)
		return val, false
	})
}

func (cm *ConcurrencyManager) Decrement(m3uIndex string) {
	cm.count.Compute(m3uIndex, func(val *atomic.Int32, loaded bool) (*atomic.Int32, bool) {
		if val == nil {
			return nil, false // Shouldn't happen if Decrement is called after Increment
		}
		// Atomic decrement with check to prevent negative values
		for {
			current := val.Load()
			if current <= 0 {
				return val, false
			}
			if val.CompareAndSwap(current, current-1) {
				break
			}
		}
		return val, false
	})
}

func (cm *ConcurrencyManager) GetCount(m3uIndex string) int {
	if val, ok := cm.count.Load(m3uIndex); ok {
		return int(val.Load())
	}
	return 0
}
