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

	maxBufferSize := 100 * 1024 * 1024

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
				b.mu.Lock()
				defer b.mu.Unlock()

				pos := b.clientPositions[clientID]
				if len(b.data) >= bufferSize {
					chunk := b.data[pos : pos+bufferSize]

					// Send the chunk to the client
					b.clients[clientID] <- chunk

					// Update the client's position after sending the chunk
					b.clientPositions[clientID] += bufferSize

					// Check if the buffer exceeds the maximum allowed size
					if len(b.data) > maxBufferSize {
						// Calculate how much we need to trim from the buffer
						trimSize := len(b.data) - maxBufferSize

						// Remove the oldest `trimSize` data from the buffer
						b.data = b.data[trimSize:]

						// Adjust all clients' positions since the buffer was trimmed
						for id, pos := range b.clientPositions {
							if pos < trimSize {
								// Client is behind the trimmed portion, adjust it to the new start of the buffer
								b.clientPositions[id] = 0
							} else {
								// Adjust the client's position relative to the new buffer start
								b.clientPositions[id] -= trimSize
							}
						}
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
