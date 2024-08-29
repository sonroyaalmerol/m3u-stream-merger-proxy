package database

import (
	"fmt"
	"m3u-stream-merger/utils"
	"os"
	"strconv"
)

func (db *Instance) ConcurrencyPriorityValue(m3uIndex int) int {
	maxConcurrency, err := strconv.Atoi(os.Getenv(fmt.Sprintf("M3U_MAX_CONCURRENCY_%d", m3uIndex+1)))
	if err != nil {
		maxConcurrency = 1
	}

	count, err := db.GetConcurrency(m3uIndex)
	if err != nil {
		count = 0
	}

	return maxConcurrency - count
}

func (db *Instance) CheckConcurrency(m3uIndex int) bool {
	maxConcurrency, err := strconv.Atoi(os.Getenv(fmt.Sprintf("M3U_MAX_CONCURRENCY_%d", m3uIndex+1)))
	if err != nil {
		maxConcurrency = 1
	}

	count, err := db.GetConcurrency(m3uIndex)
	if err != nil {
		utils.SafeLogf("Error checking concurrency: %s\n", err.Error())
		return false
	}

	utils.SafeLogf("Current number of connections for M3U_%d: %d", m3uIndex+1, count)
	return count >= maxConcurrency
}

func (db *Instance) UpdateConcurrency(m3uIndex int, incr bool) {
	var err error
	if incr {
		err = db.IncrementConcurrency(m3uIndex)
	} else {
		err = db.DecrementConcurrency(m3uIndex)
	}
	if err != nil {
		utils.SafeLogf("Error updating concurrency: %s\n", err.Error())
	}

	count, err := db.GetConcurrency(m3uIndex)
	if err != nil {
		utils.SafeLogf("Error checking concurrency: %s\n", err.Error())
	}
	utils.SafeLogf("Current number of connections for M3U_%d: %d", m3uIndex+1, count)
}
