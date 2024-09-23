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
	clients         map[int]*chan []byte
	clientPositions map[int]int
	slowestClient   int
	clientNextId    int
	ingest          sync.Mutex
	mu              sync.Mutex
}

// TODO: move buffers to Redis
var globalBuffers map[string]*Buffer

func NewBuffer() *Buffer {
	buf := &Buffer{
		data:            []byte{},
		clients:         make(map[int]*chan []byte),
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

func (b *Buffer) Subscribe(ctx context.Context) *chan []byte {
	b.mu.Lock()

	clientID := b.clientNextId
	b.clientNextId++

	ch := make(chan []byte)
	b.clients[clientID] = &ch

	bufferSize := 1024
	bufferMbInt, err := strconv.Atoi(os.Getenv("BUFFER_MB"))
	if err == nil && bufferMbInt > 0 {
		bufferSize = bufferMbInt * 1024 * 1024
	}

	b.mu.Unlock()

	go func() {
		for {
			select {
			case <-ctx.Done():
				b.mu.Lock()
				if ch, exists := b.clients[clientID]; exists {
					close(*ch) // close the channel when unsubscribing
					delete(b.clients, clientID)
					delete(b.clientPositions, clientID)
				}
				if len(b.clients) == 0 {
					b.data = nil // Reset the buffer to empty
					b.data = []byte{}
				}

				b.mu.Unlock()

				return
			default:
				b.mu.Lock()
				pos, ok := b.clientPositions[clientID]

				if !ok {
					pos = 0
				}

				if len(b.data) >= pos+bufferSize {
					chunk := b.data[pos : pos+bufferSize]
					*b.clients[clientID] <- chunk
					b.clientPositions[clientID] += bufferSize

					if b.clientPositions[clientID] < b.clientPositions[b.slowestClient] {
						b.slowestClient = clientID
					}

					b.data = b.data[b.clientPositions[b.slowestClient]:]

					for id := range b.clientPositions {
						b.clientPositions[id] -= b.clientPositions[b.slowestClient]
					}
				}

				b.mu.Unlock()
			}
		}
	}()

	return &ch
}
