package proxy

import (
	"sync"
)

// Buffer to store incoming data
type Buffer struct {
	data          []byte
	testedIndexes []int
	clients       int
	ingest        sync.Mutex
	mu            sync.Mutex
	cond          *sync.Cond // Condition variable for signaling when data is available
}

// TODO: move buffers to Redis
var globalBuffers map[string]*Buffer

func NewBuffer() *Buffer {
	buf := &Buffer{
		data:          []byte{},
		testedIndexes: []int{},
	}
	buf.cond = sync.NewCond(&buf.mu)
	return buf
}

func (b *Buffer) Write(data []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, data...)
	b.cond.Broadcast() // Notify all waiting clients that new data is available
}

func (b *Buffer) ReadChunk(size int, force bool) ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Wait for buffer to have enough data
	if !force {
		for len(b.data) < size {
			b.cond.Wait()
		}
	}

	chunk := b.data[:size]
	b.data = b.data[size:] // Remove the chunk from the buffer
	return chunk, true
}

func (b *Buffer) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = nil // Reset the buffer to empty
	b.data = []byte{}
}
