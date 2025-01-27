package utils

import (
	"sync"
	"os"
	"strconv"
	"runtime"
)
// BufferPool manages reusable buffers of specified sizes.
type BufferPool struct {
	pools []sync.Pool
}

// NewBufferPool creates a new BufferPool instance with local pools for each CPU core.
func NewBufferPool() *BufferPool {
	numPools := runtime.GOMAXPROCS(0)
	pools := make([]sync.Pool, numPools)
	for i := range pools {
		pools[i] = sync.Pool{
			New: func() interface{} {
				return make([]byte, 0)
			},
		}
	}
	return &BufferPool{
		pools: pools,
	}
}

// getLocalPool retrieves the local pool for the current goroutine using GOMAXPROCS.
func (bp *BufferPool) getLocalPool() *sync.Pool {
	pid := runtime_procID() % len(bp.pools)
	return &bp.pools[pid]
}

// Get acquires a buffer of the specified size (in MB) from the local pool.
func (bp *BufferPool) Get() []byte {

	// get he configured buffer size in MB
	bufferMbInt, err := strconv.Atoi(os.Getenv("BUFFER_MB"))
	if err != nil || bufferMbInt < 0 {
		bufferMbInt = 0
	}
	// setup the actual size of the buffer, defaul to 1Kb
	size := 1024
	if bufferMbInt > 0 {
		size = bufferMbInt * 1024 * 1024 // Convert MB to bytes
	}
	
	// setup the local pool of buffers
	pool := bp.getLocalPool()
	buf := pool.Get().([]byte)

	// Ensure the buffer is at least the requested size.
	if cap(buf) < size {
		buf = make([]byte, size)
	} else {
		buf = buf[:size]
	}

	// return the buffer
	return buf
}

// Put releases the buffer back into the local pool for reuse.
func (bp *BufferPool) Put(buf []byte) {
	// Optionally, you could reset the buffer to zero-length for security reasons.
	for i := range buf {
		buf[i] = 0
	}
	pool := bp.getLocalPool()
	pool.Put(buf[:0])
}

// runtime_procID generates a pseudo-unique identifier for the current goroutine.
func runtime_procID() int {
	return runtime.GOMAXPROCS(0)
}
