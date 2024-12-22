package store

import (
	"fmt"
	"m3u-stream-merger/utils"
	"os"
	"strconv"
	"sync"
)

type ConcurrencyManager struct {
	mu    sync.Mutex
	count map[string]int
}

func NewConcurrencyManager() *ConcurrencyManager {
	return &ConcurrencyManager{count: make(map[string]int)}
}

func (cm *ConcurrencyManager) Increment(m3uIndex string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.count[m3uIndex]++
}

func (cm *ConcurrencyManager) Decrement(m3uIndex string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if cm.count[m3uIndex] > 0 {
		cm.count[m3uIndex]--
	}
}

func (cm *ConcurrencyManager) GetCount(m3uIndex string) int {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	return cm.count[m3uIndex]
}

func (cm *ConcurrencyManager) ConcurrencyPriorityValue(m3uIndex string) int {
	maxConcurrency, err := strconv.Atoi(os.Getenv(fmt.Sprintf("M3U_MAX_CONCURRENCY_%s", m3uIndex)))
	if err != nil {
		maxConcurrency = 1
	}

	count := cm.GetCount(m3uIndex)

	return maxConcurrency - count
}

func (cm *ConcurrencyManager) CheckConcurrency(m3uIndex string) bool {
	maxConcurrency, err := strconv.Atoi(os.Getenv(fmt.Sprintf("M3U_MAX_CONCURRENCY_%s", m3uIndex)))
	if err != nil {
		maxConcurrency = 1
	}

	count := cm.GetCount(m3uIndex)

	utils.SafeLogf("Current number of connections for M3U_%s: %d", m3uIndex, count)
	return count >= maxConcurrency
}

func (cm *ConcurrencyManager) UpdateConcurrency(m3uIndex string, incr bool) {
	if incr {
		cm.Increment(m3uIndex)
	} else {
		cm.Decrement(m3uIndex)
	}

	count := cm.GetCount(m3uIndex)

	utils.SafeLogf("Current number of connections for M3U_%s: %d", m3uIndex, count)
}
