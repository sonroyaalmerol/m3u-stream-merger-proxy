package proxy

import (
	"context"
	"os"
	"strconv"
	"sync"
)

// Buffer to store incoming data
type Buffer struct {
	data            []byte
	testedIndexes   []int
	clients         map[int]chan []byte
	clientPositions map[int]int
	clientNextId    int
	ingest          sync.Mutex
	mu              sync.Mutex
}

// TODO: move buffers to Redis
var globalBuffers map[string]*Buffer

func NewBuffer() *Buffer {
	buf := &Buffer{
		data:            []byte{},
		clients:         make(map[int]chan []byte),
		clientPositions: make(map[int]int),
		testedIndexes:   []int{},
	}
	return buf
}

func (b *Buffer) Write(data []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.data = append(b.data, data...)
}

func (b *Buffer) Subscribe(ctx context.Context) chan []byte {
	clientID := b.clientNextId
	b.clientNextId++

	ch := make(chan []byte)
	b.clients[clientID] = ch

	bufferSize := 1024
	bufferMbInt, err := strconv.Atoi(os.Getenv("BUFFER_MB"))
	if err == nil && bufferMbInt > 0 {
		bufferSize = bufferMbInt * 1024 * 1024
	}

	maxBufferSize := 100 * 1024 * 1024

	go func() {
		for {
			select {
			case <-ctx.Done():
				if ch, exists := b.clients[clientID]; exists {
					close(ch) // close the channel when unsubscribing
					delete(b.clients, clientID)
					delete(b.clientPositions, clientID)
				}
				if len(b.clients) == 0 {
					b.Clear()
				}

				return
			default:
				pos, ok := b.clientPositions[clientID]
				if !ok {
					pos = 0
				}

				if len(b.data) >= bufferSize {
					chunk := b.data[pos : pos+bufferSize]
					b.clients[clientID] <- chunk
					b.clientPositions[clientID] += bufferSize

					if len(b.data) > maxBufferSize && b.mu.TryLock() {
						trimSize := len(b.data) - maxBufferSize
						b.data = b.data[trimSize:]

						for id, pos := range b.clientPositions {
							if pos < trimSize {
								b.clientPositions[id] = 0
							} else {
								b.clientPositions[id] -= trimSize
							}
						}

						b.mu.Unlock()
					}
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
