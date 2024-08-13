package database

import (
	"fmt"
	"log"
	"os"
	"strconv"
)

func (db *Instance) ConcurrencyPriorityValue(m3uIndex int) int {
	maxConcurrency, err := strconv.Atoi(os.Getenv(fmt.Sprintf("M3U_MAX_CONCURRENCY_%d", m3uIndex)))
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
	maxConcurrency, err := strconv.Atoi(os.Getenv(fmt.Sprintf("M3U_MAX_CONCURRENCY_%d", m3uIndex)))
	if err != nil {
		maxConcurrency = 1
	}

	count, err := db.GetConcurrency(m3uIndex)
	if err != nil {
		log.Printf("Error checking concurrency: %s\n", err.Error())
		return false
	}

	log.Printf("Current number of connections for M3U_%d: %d", m3uIndex, count)
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
		log.Printf("Error updating concurrency: %s\n", err.Error())
	}

	count, err := db.GetConcurrency(m3uIndex)
	if err != nil {
		log.Printf("Error checking concurrency: %s\n", err.Error())
	}
	log.Printf("Current number of connections for M3U_%d: %d", m3uIndex, count)
}
