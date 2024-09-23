package proxy

import (
	"context"
	"os"
	"strconv"
	"sync"
)

// Buffer to store incoming data
type Buffer struct {
	ctx           context.Context
	CloseStream   context.CancelFunc
	data          []byte
	testedIndexes []int
	clients       map[int]chan []byte
	clientCounter int
	ingest        sync.Mutex
	mu            sync.Mutex
	cond          *sync.Cond // Condition variable for signaling when data is available
	streaming     bool
}

// TODO: move buffers to Redis
var globalBuffers map[string]*Buffer

func NewBuffer() *Buffer {
	ctx, cancel := context.WithCancel(context.Background())
	buf := &Buffer{
		ctx:           ctx,
		CloseStream:   cancel,
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

func (b *Buffer) StreamToClients() {
	bufferSize := 1024
	bufferMbInt, err := strconv.Atoi(os.Getenv("BUFFER_MB"))
	if err == nil && bufferMbInt > 0 {
		bufferSize = bufferMbInt * 1024 * 1024
	}

	go func() {
		for {
			select {
			case <-b.ctx.Done():
				return
			default:
				if len(b.data) >= bufferSize {
					chunk := b.data[:bufferSize]
					b.data = b.data[bufferSize:] // Remove the chunk from the buffer

					for _, client := range b.clients {
						client <- chunk
					}
				}
			}
		}
	}()
}

func (b *Buffer) Subscribe() (int, chan []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.streaming {
		b.StreamToClients()
		b.streaming = true
	}

	clientID := b.clientCounter
	b.clientCounter++

	ch := make(chan []byte)
	b.clients[clientID] = ch

	return clientID, ch
}

func (b *Buffer) Unsubscribe(clientID int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if ch, exists := b.clients[clientID]; exists {
		close(ch) // close the channel when unsubscribing
		delete(b.clients, clientID)
	}

	if len(b.clients) == 0 {
		b.Clear()
	}
}

func (b *Buffer) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.data = nil // Reset the buffer to empty
	b.data = []byte{}

	b.streaming = false
	b.CloseStream()

	ctx, cancel := context.WithCancel(context.Background())
	b.ctx = ctx
	b.CloseStream = cancel
}
