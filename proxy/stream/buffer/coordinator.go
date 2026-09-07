package buffer

import (
	"container/ring"
	"context"
	"errors"
	"fmt"
	"io"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/proxy/loadbalancer"
	"m3u-stream-merger/proxy/stream/config"
	"m3u-stream-merger/store"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrStreamClosed   = errors.New("stream is closed")
	ErrStreamDraining = errors.New("stream is draining")
)

type ChunkData struct {
	Data      []byte
	Error     error
	Status    int
	Timestamp time.Time
	seq       int64
}

// Seq exposes the stream sequence number of the chunk.
func (c *ChunkData) Seq() int64 { return c.seq }

// Internal state constants.
const (
	stateActive int32 = iota
	stateDraining
	stateClosed
)

type StreamCoordinator struct {
	Buffer       *ring.Ring
	Mu           sync.RWMutex
	broadcast    chan struct{}
	ClientCount  int32
	WriterCtx    context.Context
	WriterCancel context.CancelFunc
	WriterCtxMu  sync.Mutex
	WriterActive atomic.Bool

	WriterRespHeader atomic.Pointer[http.Header]
	respHeaderSet    atomic.Pointer[chan struct{}]
	headerMu         sync.Mutex
	headerSignaled   bool
	m3uHeaderSet     atomic.Bool

	LastError atomic.Value
	logger    logger.Logger
	config    *config.StreamConfig
	cm        *store.ConcurrencyManager
	streamID  string

	InitializationMu sync.Mutex

	// state represents active, draining, or closed.
	state int32

	LBResultOnWrite  atomic.Pointer[loadbalancer.LoadBalancerResult]
	lastProcessedSeq atomic.Int64

	writeSeq int64

	droppedChunks  atomic.Int64
	capacityWarned atomic.Bool
}

// notifyLocked wakes waiting readers. Caller must already hold c.Mu for writing.
func (c *StreamCoordinator) notifyLocked() {
	close(c.broadcast)
	c.broadcast = make(chan struct{})
}

// notifySubscribers closes the current broadcast channel and
// creates a new one so waiting clients can be notified.
func (c *StreamCoordinator) notifySubscribers() {
	c.Mu.Lock()
	c.notifyLocked()
	c.Mu.Unlock()
}

func NewStreamCoordinator(streamID string, config *config.StreamConfig, cm *store.ConcurrencyManager, logger logger.Logger) *StreamCoordinator {
	logger.Debug("Initializing new StreamCoordinator")
	r := ring.New(config.SharedBufferSize)

	respHeaderChan := make(chan struct{})
	coord := &StreamCoordinator{
		Buffer:    r,
		logger:    logger,
		config:    config,
		cm:        cm,
		streamID:  streamID,
		broadcast: make(chan struct{}),
	}
	coord.respHeaderSet.Store(&respHeaderChan)
	atomic.StoreInt32(&coord.state, stateActive)
	coord.LastError.Store((*ChunkData)(nil))

	logger.Debugf("StreamCoordinator initialized with buffer size: %d, chunk size: %d",
		config.SharedBufferSize, config.ChunkSize)
	return coord
}

// resetHeaderChan swaps in a fresh header-notify channel, closing the
// previous one at most once so waiters are released without a double close.
func (c *StreamCoordinator) resetHeaderChan() {
	c.headerMu.Lock()
	defer c.headerMu.Unlock()

	newCh := make(chan struct{})
	if old := c.respHeaderSet.Swap(&newCh); old != nil && !c.headerSignaled {
		close(*old)
	}
	c.headerSignaled = false
}

// signalHeaderChan closes the current header-notify channel exactly once,
// waking WaitHeaders callers. No-op if it was already closed or swapped out.
func (c *StreamCoordinator) signalHeaderChan() {
	c.headerMu.Lock()
	defer c.headerMu.Unlock()

	ch := c.respHeaderSet.Load()
	if ch == nil || c.headerSignaled {
		return
	}
	c.headerSignaled = true
	close(*ch)
}

func (c *StreamCoordinator) WaitHeaders(ctx context.Context) {
	for c.WriterRespHeader.Load() == nil {
		ch := c.respHeaderSet.Load()
		if ch == nil {
			return
		}
		select {
		case <-*ch:
		case <-ctx.Done():
			return
		}
	}
}

// GetWriterLBResult returns the load balancer result for the current writer call.
func (c *StreamCoordinator) GetWriterLBResult() *loadbalancer.LoadBalancerResult {
	return c.LBResultOnWrite.Load()
}

// RegisterClient registers a new client and returns an error if the stream
// is no longer active.
func (c *StreamCoordinator) RegisterClient() error {
	c.Mu.Lock()
	defer c.Mu.Unlock()

	state := atomic.LoadInt32(&c.state)
	clientCount := atomic.LoadInt32(&c.ClientCount)

	// If stream is closed but there are no clients, allow reset
	if state != stateActive && clientCount == 0 {
		c.logger.Debug("Resetting closed stream to active state")
		atomic.StoreInt32(&c.state, stateActive)

		// Reset error state
		c.LastError.Store((*ChunkData)(nil))
		c.resetHeaderChan()
	}

	count := atomic.AddInt32(&c.ClientCount, 1)
	c.logger.Debugf("Client registered. Total clients: %d", count)
	return nil
}

// UnregisterClient unregisters a client and cleans up resources if it was the last.
func (c *StreamCoordinator) UnregisterClient() {
	count := atomic.AddInt32(&c.ClientCount, -1)
	c.logger.Logf("Client unregistered (%s). Remaining clients: %d", c.streamID, count)
	if count == 0 {
		c.logger.Log("Last client unregistered, cleaning up resources")
		atomic.StoreInt32(&c.state, stateDraining)
		c.WriterRespHeader.Store(nil)
		c.ClearBuffer()
		c.notifySubscribers()
	}
}

func (c *StreamCoordinator) HasClient() bool {
	return atomic.LoadInt32(&c.ClientCount) > 0
}

// shouldTimeout checks if the time since the last successful read exceeds the timeout.
func (c *StreamCoordinator) shouldTimeout(lastSuccess time.Time, timeout time.Duration) bool {
	shouldTimeout := c.config.TimeoutSeconds > 0 && time.Since(lastSuccess) >= timeout
	if shouldTimeout {
		c.logger.Debugf("Stream timed out after %v", time.Since(lastSuccess))
	}
	return shouldTimeout
}

// shouldTimeout reports whether no data has been read for a full timeout window.
func (c *StreamCoordinator) Write(chunk *ChunkData) bool {
	if chunk == nil {
		c.logger.Debug("Write: Received nil chunk")
		return false
	}

	c.Mu.Lock()
	if atomic.LoadInt32(&c.state) != stateActive {
		c.logger.Debug("Write: Stream not active")
		c.Mu.Unlock()
		return false
	}

	chunk.seq = atomic.AddInt64(&c.writeSeq, 1)
	c.Buffer.Value = chunk
	c.Buffer = c.Buffer.Next()
	c.logger.Debug("Write: Advanced buffer position")

	if chunk.Error != nil || chunk.Status != 0 {
		if c.LastError.Load() == nil {
			c.LastError.Store(chunk)
		}
		atomic.StoreInt32(&c.state, stateDraining)
		c.logger.Debugf("Write: Setting error state: err=%v, status=%d", chunk.Error, chunk.Status)
	}

	c.notifyLocked()
	c.Mu.Unlock()
	return true
}

// writeChunk publishes b and takes ownership of it; the caller must not reuse b.
func (c *StreamCoordinator) writeChunk(b []byte) error {
	c.Write(&ChunkData{Data: b, Timestamp: time.Now()})
	return nil
}

// InitialPosition returns the ring position a new reader should start from.
func (c *StreamCoordinator) InitialPosition() *ring.Ring {
	c.Mu.RLock()
	defer c.Mu.RUnlock()
	return c.Buffer.Prev()
}

// ResumePosition returns the ring slot holding the first chunk published
// after lastSeq, so a reader can continue where it stopped across writer
// restarts. ok is false when that data is no longer in the ring.
func (c *StreamCoordinator) ResumePosition(lastSeq int64) (*ring.Ring, bool) {
	c.Mu.RLock()
	defer c.Mu.RUnlock()

	current := c.Buffer.Next()
	for i := 0; i < c.config.SharedBufferSize; i++ {
		if chunk, ok := current.Value.(*ChunkData); ok && chunk != nil && chunk.seq > lastSeq {
			return current, true
		}
		current = current.Next()
	}
	return nil, false
}

// EnsureActiveForWriter transitions the coordinator to the active state
// for a writer that is about to start, clearing any terminal state left by
// the previous writer. Stale error-marker chunks are wiped (they carry no
// data) while data chunks are preserved so readers can resume.
func (c *StreamCoordinator) EnsureActiveForWriter() {
	c.Mu.Lock()
	current := c.Buffer
	for i := 0; i < c.config.SharedBufferSize; i++ {
		if chunk, ok := current.Value.(*ChunkData); ok && chunk != nil &&
			(chunk.Error != nil || chunk.Status != 0) {
			current.Value = (*ChunkData)(nil)
		}
		current = current.Next()
	}
	if atomic.LoadInt32(&c.state) != stateActive {
		atomic.StoreInt32(&c.state, stateActive)
		c.LastError.Store((*ChunkData)(nil))
		c.resetHeaderChan()
	}
	c.Mu.Unlock()
}

// ReadChunks retrieves chunks from the ring for a client, given a starting position.
// clientSeq is the sequence number of the last chunk the client successfully read;
// pass 0 on the first call. The returned int64 is the updated sequence to pass next time.
func (c *StreamCoordinator) ReadChunks(ctx context.Context, fromPosition *ring.Ring, clientSeq int64) (
	[]*ChunkData, *ChunkData, *ring.Ring, int64, bool,
) {
	rejoined := false
	c.Mu.RLock()
	if fromPosition == nil {
		c.logger.Debug("ReadChunks: fromPosition is nil, using current buffer")
		fromPosition = c.Buffer
	}

	if clientSeq > 0 {
		currentWriteSeq := atomic.LoadInt64(&c.writeSeq)
		if clientSeq < currentWriteSeq-int64(c.config.SharedBufferSize) {
			jumped := currentWriteSeq - clientSeq
			c.droppedChunks.Add(jumped)
			c.logger.Logf("Stream %s: reader lagged behind by %d chunks; rejoining at live edge",
				c.streamID, jumped)
			fromPosition = c.Buffer
			rejoined = true
		}
	}

	for fromPosition == c.Buffer && atomic.LoadInt32(&c.state) == stateActive {
		ch := c.broadcast
		c.Mu.RUnlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, nil, fromPosition, clientSeq, rejoined
		}
		c.Mu.RLock()
	}

	var chunks []*ChunkData
	current := fromPosition
	var errorChunk *ChunkData
	newClientSeq := clientSeq

	for current != c.Buffer {
		if chunk, ok := current.Value.(*ChunkData); ok && chunk != nil {
			if len(chunk.Data) > 0 {
				if chunks == nil {
					chunks = make([]*ChunkData, 0, c.config.SharedBufferSize)
				}
				chunks = append(chunks, chunk)
				if chunk.seq > newClientSeq {
					newClientSeq = chunk.seq
				}
			}
			if chunk.Error != nil || chunk.Status != 0 {
				errorChunk = chunk
			}
		}
		current = current.Next()
		if current == fromPosition {
			break
		}
	}
	c.Mu.RUnlock()

	if errorChunk != nil {
		return chunks, errorChunk, current, newClientSeq, rejoined
	}

	if lastErr := c.LastError.Load(); lastErr != nil {
		if errChunk, ok := lastErr.(*ChunkData); ok && errChunk != nil {
			return chunks, errChunk, current, newClientSeq, rejoined
		}
	}

	return chunks, nil, current, newClientSeq, rejoined
}

func (c *StreamCoordinator) ClearBuffer() {
	c.Mu.Lock()
	defer c.Mu.Unlock()

	current := c.Buffer
	for i := 0; i < c.config.SharedBufferSize; i++ {
		current.Value = (*ChunkData)(nil)
		current = current.Next()
	}
}

// getTimeoutDuration returns the streaming timeout duration.
func (c *StreamCoordinator) getTimeoutDuration() time.Duration {
	if c.config.TimeoutSeconds == 0 {
		return time.Minute
	}
	return time.Duration(c.config.TimeoutSeconds) * time.Second
}

func (c *StreamCoordinator) writeError(err error, status int) {
	chunk := &ChunkData{
		Error:     err,
		Status:    status,
		Timestamp: time.Now(),
	}
	c.Write(chunk)
	atomic.StoreInt32(&c.state, stateClosed)
}

func (c *StreamCoordinator) readAndWriteStream(
	ctx context.Context,
	body io.ReadCloser,
	processChunk func([]byte) error,
) error {
	var slab []byte
	timeout := c.getTimeoutDuration()
	lastSuccess := time.Now()
	zeroReads := 0

	var totalBytesRead int64
	lastHealthLog := time.Now()

	for atomic.LoadInt32(&c.state) == stateActive {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			if c.shouldTimeout(lastSuccess, timeout) {
				return ErrStreamTimeout
			}

			if len(slab) < c.config.ChunkSize/4+1 {
				slab = make([]byte, c.config.ChunkSize)
			}

			n, err := body.Read(slab)
			if n == 0 {
				if err != nil {
					return err
				}
				zeroReads++
				if zeroReads > 10 {
					return io.EOF
				}
				time.Sleep(10 * time.Millisecond)
				continue
			}

			zeroReads = 0
			totalBytesRead += int64(n)

			if window := time.Since(lastHealthLog); window >= 2*time.Second {
				windowThroughput := float64(totalBytesRead) / window.Seconds()
				c.logger.Debugf("Buffer health: throughput = %.2f Bps (2s window)", windowThroughput)
				totalBytesRead = 0
				lastHealthLog = time.Now()

				if c.config.ExpectedThroughput > 0 &&
					windowThroughput < float64(c.config.ExpectedThroughput) {
					c.logger.Warnf("Low buffer health: throughput %.2f Bps below expected %d Bps",
						windowThroughput, c.config.ExpectedThroughput,
					)
					return fmt.Errorf("low buffer health: %.2f Bps", windowThroughput)
				}

				ringBytes := int64(c.config.SharedBufferSize) * int64(c.config.ChunkSize)
				if need := int64(windowThroughput * timeout.Seconds()); need > ringBytes && !c.capacityWarned.Swap(true) {
					suggested := (need + int64(c.config.ChunkSize) - 1) / int64(c.config.ChunkSize)
					c.logger.Warnf("Stream %s: sustained ~%.0f Bps needs ~%.0fs of buffer but the ring holds only %.1fs; raise BUFFER_CHUNK_NUM to >= %d or lower STREAM_TIMEOUT",
						c.streamID, windowThroughput, timeout.Seconds(),
						float64(ringBytes)/windowThroughput, suggested)
				}
			}

			chunk := slab[:n:n]
			slab = slab[n:]

			if err == io.EOF && n > 0 {
				if err = processChunk(chunk); err != nil {
					return err
				}
				return io.EOF
			}

			if err != nil {
				// Body read errors are terminal; retrying belongs to the
				// handler, which re-runs the load balancer.
				return err
			}

			if err = processChunk(chunk); err != nil {
				return err
			}
			// Paced publishing can take seconds; it is progress, not a stall.
			lastSuccess = time.Now()
		}
	}
	return nil
}
