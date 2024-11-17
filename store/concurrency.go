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
	count map[int]int
}

func NewConcurrencyManager() *ConcurrencyManager {
	return &ConcurrencyManager{count: make(map[int]int)}
}

func (cm *ConcurrencyManager) Increment(m3uIndex int) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.count[m3uIndex]++
}

func (cm *ConcurrencyManager) Decrement(m3uIndex int) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cm.count[m3uIndex] > 0 {
		cm.count[m3uIndex]--
	}
}

func (cm *ConcurrencyManager) GetCount(m3uIndex int) int {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.count[m3uIndex]
}

func (cm *ConcurrencyManager) ConcurrencyPriorityValue(m3uIndex int) int {
	maxConcurrency, err := strconv.Atoi(os.Getenv(fmt.Sprintf("M3U_MAX_CONCURRENCY_%d", m3uIndex+1)))
	if err != nil {
		maxConcurrency = 1
	}

	count := cm.GetCount(m3uIndex)

	return maxConcurrency - count
}

func (cm *ConcurrencyManager) CheckConcurrency(m3uIndex int) bool {
	maxConcurrency, err := strconv.Atoi(os.Getenv(fmt.Sprintf("M3U_MAX_CONCURRENCY_%d", m3uIndex+1)))
	if err != nil {
		maxConcurrency = 1
	}

	count := cm.GetCount(m3uIndex)

	utils.SafeLogf("Current number of connections for M3U_%d: %d", m3uIndex+1, count)
	return count >= maxConcurrency
}

func (cm *ConcurrencyManager) UpdateConcurrency(m3uIndex int, incr bool) {
	if incr {
		cm.Increment(m3uIndex)
	} else {
		cm.Decrement(m3uIndex)
	}

	count := cm.GetCount(m3uIndex)
	utils.SafeLogf("Current number of connections for M3U_%d: %d", m3uIndex+1, count)
}
