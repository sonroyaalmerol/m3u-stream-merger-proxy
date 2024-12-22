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
	count map[string]map[string]int
}

func NewConcurrencyManager() *ConcurrencyManager {
	return &ConcurrencyManager{count: make(map[string]map[string]int)}
}

func (cm *ConcurrencyManager) Increment(m3uIndex string, subIndex string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if _, ok := cm.count[m3uIndex]; !ok {
		cm.count[m3uIndex] = make(map[string]int)
	}

	cm.count[m3uIndex][subIndex]++
}

func (cm *ConcurrencyManager) Decrement(m3uIndex string, subIndex string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if _, ok := cm.count[m3uIndex]; !ok {
		cm.count[m3uIndex] = make(map[string]int)
	}

	if cm.count[m3uIndex][subIndex] > 0 {
		cm.count[m3uIndex][subIndex]--
	}
}

func (cm *ConcurrencyManager) GetCount(m3uIndex string, subIndex string) int {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if _, ok := cm.count[m3uIndex]; !ok {
		cm.count[m3uIndex] = make(map[string]int)
	}

	return cm.count[m3uIndex][subIndex]
}

func (cm *ConcurrencyManager) ConcurrencyPriorityValue(m3uIndex string, subIndex string) int {
	maxConcurrency, err := strconv.Atoi(os.Getenv(fmt.Sprintf("M3U_MAX_CONCURRENCY_%s", m3uIndex)))
	if err != nil {
		maxConcurrency = 1
	}

	count := cm.GetCount(m3uIndex, subIndex)

	return maxConcurrency - count
}

func (cm *ConcurrencyManager) CheckConcurrency(m3uIndex string, subIndex string) bool {
	maxConcurrency, err := strconv.Atoi(os.Getenv(fmt.Sprintf("M3U_MAX_CONCURRENCY_%s", m3uIndex)))
	if err != nil {
		maxConcurrency = 1
	}

	count := cm.GetCount(m3uIndex, subIndex)

	utils.SafeLogf("Current number of connections for M3U_%s|%s: %d", m3uIndex, subIndex, count)
	return count >= maxConcurrency
}

func (cm *ConcurrencyManager) UpdateConcurrency(m3uIndex string, subIndex string, incr bool) {
	if incr {
		cm.Increment(m3uIndex, subIndex)
	} else {
		cm.Decrement(m3uIndex, subIndex)
	}

	count := cm.GetCount(m3uIndex, subIndex)
	utils.SafeLogf("Current number of connections for M3U_%s|%s: %d", m3uIndex, subIndex, count)
}
