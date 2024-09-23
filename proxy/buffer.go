package proxy

import (
	"context"
	"os"
	"strconv"
	"sync"
)

// Buffer to store incoming data
type Buffer struct {
	data          []byte
	testedIndexes []int
	clients       map[int]chan []byte
	clientNextId  int
	ingest        sync.Mutex
	mu            sync.Mutex
	cond          *sync.Cond // Condition variable for signaling when data is available
}

// TODO: move buffers to Redis
var globalBuffers map[string]*Buffer

func NewBuffer() *Buffer {
	buf := &Buffer{
		data:          []byte{},
		clients:       make(map[int]chan []byte),
		testedIndexes: []int{},
	}
	buf.cond = sync.NewCond(&buf.mu)
	return buf
}

func (b *Buffer) Write(data []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.data = append(b.data, data...)
}

func (b *Buffer) Subscribe(ctx context.Context) chan []byte {
	b.mu.Lock()
	defer b.mu.Unlock()

	clientID := b.clientNextId
	b.clientNextId++

	ch := make(chan []byte)
	b.clients[clientID] = ch

	bufferSize := 1024
	bufferMbInt, err := strconv.Atoi(os.Getenv("BUFFER_MB"))
	if err == nil && bufferMbInt > 0 {
		bufferSize = bufferMbInt * 1024 * 1024
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				if ch, exists := b.clients[clientID]; exists {
					close(ch) // close the channel when unsubscribing
					delete(b.clients, clientID)
				}
				if len(b.clients) == 0 {
					b.Clear()
				}

				return
			default:
				if len(b.data) >= bufferSize {
					chunk := b.data[:bufferSize]
					b.data = b.data[bufferSize:] // Remove the chunk from the buffer

					b.clients[clientID] <- chunk
				}
			}
		}
	}()

	return ch
}

func (b *Buffer) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.data = nil // Reset the buffer to empty
	b.data = []byte{}
}
