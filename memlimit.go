package main

import (
	"log"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
)

// applyMemoryLimit points the GC at the container limit so heap growth
// becomes GC pressure instead of an OOM kill. GOMEMLIMIT env wins.
func applyMemoryLimit() {
	if os.Getenv("GOMEMLIMIT") != "" {
		return
	}
	limit, ok := cgroupMemoryLimit()
	if !ok || limit <= 0 {
		return
	}
	soft := limit * 9 / 10
	debug.SetMemoryLimit(soft)
	log.Printf("Memory limit: container %d bytes, GC soft limit %d bytes", limit, soft)
}

func cgroupMemoryLimit() (int64, bool) {
	return cgroupMemoryLimitAt("/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes")
}

func cgroupMemoryLimitAt(v2Path, v1Path string) (int64, bool) {
	if b, err := os.ReadFile(v2Path); err == nil {
		v := strings.TrimSpace(string(b))
		if v != "max" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
				return n, true
			}
		}
		return 0, false
	}
	if b, err := os.ReadFile(v1Path); err == nil {
		if n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil && n > 0 && n < 1<<40 {
			return n, true
		}
	}
	return 0, false
}
