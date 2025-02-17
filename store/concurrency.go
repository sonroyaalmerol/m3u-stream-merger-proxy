package store

import (
	"fmt"
	"m3u-stream-merger/logger"
	"os"
	"strconv"
	"sync"
)

type ConcurrencyManager struct {
	mu      sync.Mutex
	count   map[string]int
	Invalid sync.Map // map[string]struct{}{}
}

func NewConcurrencyManager() *ConcurrencyManager {
	return &ConcurrencyManager{count: make(map[string]int)}
}

func (cm *ConcurrencyManager) getMaxConcurrency(m3uIndex string) int {
	max, err := strconv.Atoi(os.Getenv(fmt.Sprintf("M3U_MAX_CONCURRENCY_%s", m3uIndex)))
	if err != nil {
		max = 1
	}
	return max
}

func (cm *ConcurrencyManager) GetConcurrencyStatus(m3uIndex string) (current int, max int, priority int) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	max = cm.getMaxConcurrency(m3uIndex)
	current = cm.count[m3uIndex]
	priority = max - current // Higher value = more available slots
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
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if incr {
		cm.count[m3uIndex]++
	} else {
		if cm.count[m3uIndex] > 0 {
			cm.count[m3uIndex]--
		}
	}

	current := cm.count[m3uIndex]
	max := cm.getMaxConcurrency(m3uIndex)
	logger.Default.Logf("Updated connections for M3U_%s: %d/%d", m3uIndex, current, max)
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
