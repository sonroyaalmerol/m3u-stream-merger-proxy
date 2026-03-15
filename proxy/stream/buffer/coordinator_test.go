package buffer

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"m3u-stream-merger/logger"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/proxy/loadbalancer"
	"m3u-stream-merger/proxy/stream/config"
	"m3u-stream-merger/store"
)

// ── test helpers ──────────────────────────────────────────────────────────────

func newCoordForTest(t *testing.T) *StreamCoordinator {
	t.Helper()
	return NewStreamCoordinator(t.Name(), &config.StreamConfig{
		SharedBufferSize: 64,
		ChunkSize:        512,
		TimeoutSeconds:   0, // no idle timeout; tests control lifetime
	}, store.NewConcurrencyManager(), logger.Default)
}

// clientRead is a minimal stand-in for HandleStream's reader half.
// It registers as a client, drains the coordinator until it receives an error
// chunk or its context is cancelled, then unregisters.
// Returns (accumulated payload, exit status).
func clientRead(ctx context.Context, coord *StreamCoordinator) ([]byte, int) {
	if err := coord.RegisterClient(); err != nil {
		return nil, proxy.StatusServerError
	}
	defer coord.UnregisterClient()

	var data []byte
	pos := coord.InitialPosition()
	var seq int64

	for {
		if ctx.Err() != nil {
			return data, proxy.StatusClientClosed
		}
		chunks, errChunk, newPos, newSeq := coord.ReadChunks(ctx, pos, seq)
		pos = newPos
		seq = newSeq
		for _, c := range chunks {
			if c != nil {
				data = append(data, c.Data...)
			}
		}
		if errChunk != nil {
			return data, errChunk.Status
		}
	}
}

// startPipeWriter starts StartMediaWriter backed by an io.Pipe so the test
// controls exactly when bytes arrive and when the stream ends.
// The pipe is synchronous: each write() call blocks until the coordinator
// has consumed the bytes, making data flow fully deterministic.
// Returns write(data), closeEOF(), writerCancel, writerDone.
func startPipeWriter(coord *StreamCoordinator) (
	write func([]byte), closeEOF func(),
	cancel context.CancelFunc, done <-chan struct{},
) {
	pr, pw := io.Pipe()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"video/MP2T"}},
		Body:       io.NopCloser(pr),
	}
	lbr := &loadbalancer.LoadBalancerResult{Response: resp, Index: "test", URL: "pipe://test"}
	ctx, cancelFn := context.WithCancel(context.Background())
	ch := make(chan struct{})
	go func() {
		defer close(ch)
		coord.StartMediaWriter(ctx, lbr)
	}()
	return func(b []byte) { _, _ = pw.Write(b) },
		func() { _ = pw.Close() },
		cancelFn,
		ch
}

// startServerWriter starts StartMediaWriter backed by a real HTTP GET request.
// Returns writerCancel, writerDone.
func startServerWriter(t *testing.T, coord *StreamCoordinator, url string) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	resp.Header.Set("Content-Type", "video/MP2T")
	lbr := &loadbalancer.LoadBalancerResult{Response: resp, Index: "test", URL: url}
	ctx, cancelFn := context.WithCancel(context.Background())
	ch := make(chan struct{})
	go func() {
		defer close(ch)
		coord.StartMediaWriter(ctx, lbr)
	}()
	return cancelFn, ch
}

// waitForClients polls until coord.ClientCount >= n or the deadline passes.
func waitForClients(coord *StreamCoordinator, n int32, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&coord.ClientCount) >= n {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

// waitForClientCount polls until coord.ClientCount == n or the deadline passes.
func waitForClientCount(coord *StreamCoordinator, n int32, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&coord.ClientCount) == n {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

// assertAllBytes fails the test if any byte in data differs from want.
// It reports only the first mismatch to keep output concise.
func assertAllBytes(t *testing.T, label string, data []byte, want byte) {
	t.Helper()
	for i, b := range data {
		if b != want {
			t.Errorf("%s: byte[%d] = %#02x, want %#02x (first mismatch in %d bytes)", label, i, b, want, len(data))
			return
		}
	}
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestCoordinator_SharedBuffer_TwoClients verifies that two simultaneous
// consumers both receive byte-identical, complete payloads through the shared
// ring buffer and that the upstream server is contacted exactly once.
//
// The server sends numChunks chunks, each chunk filled with a distinct byte
// value (0x01…0x0A) so any reordering or duplication would be caught by the
// content check.
func TestCoordinator_SharedBuffer_TwoClients(t *testing.T) {
	const chunkSize = 512
	const numChunks = 10

	// Build the exact byte sequence the server will send.
	// Chunk i contains chunkSize repetitions of byte value (i+1).
	var serverPayload []byte
	for i := range numChunks {
		serverPayload = append(serverPayload, bytes.Repeat([]byte{byte(i + 1)}, chunkSize)...)
	}

	var upstreamHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		flusher := w.(http.Flusher)
		for i := range numChunks {
			_, _ = w.Write(bytes.Repeat([]byte{byte(i + 1)}, chunkSize))
			flusher.Flush()
			time.Sleep(2 * time.Millisecond)
		}
	}))
	t.Cleanup(srv.Close)

	coord := newCoordForTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var (
		wg               sync.WaitGroup
		data1, data2     []byte
		status1, status2 int
	)
	wg.Add(2)
	go func() { defer wg.Done(); data1, status1 = clientRead(ctx, coord) }()
	go func() { defer wg.Done(); data2, status2 = clientRead(ctx, coord) }()

	// Ensure both clients are registered before the writer begins so neither
	// misses the very first ring-buffer write.
	if !waitForClients(coord, 2, time.Second) {
		t.Fatal("timed out waiting for both clients to register")
	}

	writerCancel, writerDone := startServerWriter(t, coord, srv.URL+"/stream")
	defer writerCancel()

	wg.Wait()
	<-writerDone

	for i, tc := range []struct {
		label  string
		data   []byte
		status int
	}{{"client1", data1, status1}, {"client2", data2, status2}} {
		_ = i
		if tc.status != proxy.StatusEOF {
			t.Errorf("%s: status = %d, want StatusEOF (%d)", tc.label, tc.status, proxy.StatusEOF)
		}
		if !bytes.Equal(tc.data, serverPayload) {
			t.Errorf("%s: payload mismatch — got %d bytes, want %d; first diff at byte %d",
				tc.label, len(tc.data), len(serverPayload), firstDiff(tc.data, serverPayload))
		}
	}

	// Both clients must have received identical bytes.
	if !bytes.Equal(data1, data2) {
		t.Errorf("clients received different data — shared ring buffer is inconsistent (diff at byte %d)",
			firstDiff(data1, data2))
	}

	if n := int(upstreamHits.Load()); n != 1 {
		t.Errorf("upstream connections = %d, want 1 — shared buffer not reusing the connection", n)
	}
}

// TestCoordinator_SharedBuffer_LateJoin verifies that a client joining an
// already-running stream:
//   - receives data only from its join point forward (no invented bytes)
//   - receives the correct byte values for each phase it was present for
//   - exits with StatusEOF when the upstream closes
//
// Early chunks are filled with 0xAA; late chunks with 0xBB so the two phases
// are trivially distinguishable in the received payload.
func TestCoordinator_SharedBuffer_LateJoin(t *testing.T) {
	const chunkSize = 512
	const earlyChunks = 4
	const lateChunks = 6
	const earlyByte = byte(0xAA)
	const lateByte = byte(0xBB)

	coord := newCoordForTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	earlyChunk := bytes.Repeat([]byte{earlyByte}, chunkSize)
	lateChunk := bytes.Repeat([]byte{lateByte}, chunkSize)

	write, closeEOF, writerCancel, writerDone := startPipeWriter(coord)
	defer writerCancel()

	var (
		wg               sync.WaitGroup
		data1, data2     []byte
		status1, status2 int
	)

	// Client 1 connects before any data arrives.
	wg.Add(1)
	go func() { defer wg.Done(); data1, status1 = clientRead(ctx, coord) }()
	if !waitForClients(coord, 1, time.Second) {
		t.Fatal("client1 did not register in time")
	}

	// Push early chunks — only client 1 is present.
	// The pipe is synchronous: each write() returns only after the coordinator
	// has consumed the bytes into the ring buffer.
	for range earlyChunks {
		write(earlyChunk)
	}

	// Client 2 joins mid-stream.
	wg.Add(1)
	go func() { defer wg.Done(); data2, status2 = clientRead(ctx, coord) }()
	if !waitForClients(coord, 2, time.Second) {
		t.Fatal("client2 did not register in time")
	}
	// Give client2's goroutine time to advance past RegisterClient() and call
	// InitialPosition() before we write the first late chunk. Without this
	// pause the goroutine may still be between those two calls when the first
	// late write unblocks, causing it to start one position later than expected.
	time.Sleep(10 * time.Millisecond)

	// Push late chunks — both clients receive these.
	for range lateChunks {
		write(lateChunk)
	}
	closeEOF()

	wg.Wait()
	<-writerDone

	// ── client 1: must have received all earlyChunks then all lateChunks ──
	if status1 != proxy.StatusEOF {
		t.Errorf("client1: status = %d, want StatusEOF", status1)
	}
	wantClient1 := append(
		bytes.Repeat([]byte{earlyByte}, earlyChunks*chunkSize),
		bytes.Repeat([]byte{lateByte}, lateChunks*chunkSize)...,
	)
	if !bytes.Equal(data1, wantClient1) {
		t.Errorf("client1: payload mismatch — got %d bytes, want %d (first diff at byte %d)",
			len(data1), len(wantClient1), firstDiff(data1, wantClient1))
	}

	// ── client 2: joined after earlyChunks ──
	// InitialPosition() lands on the last-written ring slot, so client2 picks
	// up at least the final early chunk plus all late chunks.  The payload
	// must end with exactly lateChunks worth of 0xBB bytes; any prefix bytes
	// must be 0xAA (the tail of the early phase, not invented data).
	if status2 != proxy.StatusEOF {
		t.Errorf("client2: status = %d, want StatusEOF", status2)
	}
	if len(data2) < lateChunks*chunkSize {
		t.Errorf("client2: received %d bytes, want >= %d", len(data2), lateChunks*chunkSize)
	}

	lateRecv := data2[len(data2)-lateChunks*chunkSize:]
	earlyRecv := data2[:len(data2)-lateChunks*chunkSize]

	assertAllBytes(t, "client2 late portion", lateRecv, lateByte)
	assertAllBytes(t, "client2 early portion", earlyRecv, earlyByte)

	t.Logf("client1=%d B, client2=%d B (%d early + %d late)", len(data1), len(data2), len(earlyRecv), len(lateRecv))
}

// TestCoordinator_SharedBuffer_EarlyDisconnect verifies that when one client
// disconnects mid-stream:
//   - the writer keeps running (its context is not cancelled)
//   - the remaining client receives all subsequent data with correct content
//   - the remaining client exits with StatusEOF
//
// Initial chunks are 0xCC; post-disconnect chunks are 0xDD.
func TestCoordinator_SharedBuffer_EarlyDisconnect(t *testing.T) {
	const chunkSize = 512
	const initialChunks = 4
	const laterChunks = 6
	const initByte = byte(0xCC)
	const lateByte = byte(0xDD)

	coord := newCoordForTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	write, closeEOF, writerCancel, writerDone := startPipeWriter(coord)
	defer writerCancel()

	client1Ctx, client1Cancel := context.WithCancel(ctx)
	var (
		wg               sync.WaitGroup
		data2            []byte
		status1, status2 int
	)
	wg.Add(2)
	go func() { defer wg.Done(); _, status1 = clientRead(client1Ctx, coord) }()
	go func() { defer wg.Done(); data2, status2 = clientRead(ctx, coord) }()

	if !waitForClients(coord, 2, time.Second) {
		t.Fatal("both clients did not register in time")
	}

	// Write initial chunks — both clients receive these.
	initChunk := bytes.Repeat([]byte{initByte}, chunkSize)
	for range initialChunks {
		write(initChunk)
	}

	// Drop client 1. The pipe is synchronous so all initialChunks writes
	// are already in the ring by the time we reach this line.
	client1Cancel()
	if !waitForClientCount(coord, 1, time.Second) {
		t.Errorf("ClientCount = %d after client1 disconnect, want 1",
			atomic.LoadInt32(&coord.ClientCount))
	}

	// Write later chunks — only client 2 receives these.
	lateChunk := bytes.Repeat([]byte{lateByte}, chunkSize)
	for range laterChunks {
		write(lateChunk)
	}
	closeEOF()

	wg.Wait()
	<-writerDone

	if status1 != proxy.StatusClientClosed {
		t.Errorf("client1: status = %d, want StatusClientClosed", status1)
	}
	if status2 != proxy.StatusEOF {
		t.Errorf("client2: status = %d, want StatusEOF", status2)
	}

	// client2 must have received every byte in the correct order.
	wantData2 := append(
		bytes.Repeat([]byte{initByte}, initialChunks*chunkSize),
		bytes.Repeat([]byte{lateByte}, laterChunks*chunkSize)...,
	)
	if !bytes.Equal(data2, wantData2) {
		t.Errorf("client2: payload mismatch — got %d bytes, want %d (first diff at byte %d)",
			len(data2), len(wantData2), firstDiff(data2, wantData2))
	}

	t.Logf("client2 received %d bytes (%d init + %d late)", len(data2),
		initialChunks*chunkSize, laterChunks*chunkSize)
}

// TestCoordinator_SharedBuffer_WriterStopsAfterLastClient verifies that the
// writer goroutine exits promptly when its context is cancelled (replicating
// what HandleStream.cleanup does once ClientCount reaches zero) and that the
// client actually received real data — not an empty stream.
func TestCoordinator_SharedBuffer_WriterStopsAfterLastClient(t *testing.T) {
	const streamByte = byte(0xDD)

	// Infinite upstream — only stops when the request context is cancelled.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		chunk := bytes.Repeat([]byte{streamByte}, 512)
		for {
			select {
			case <-r.Context().Done():
				return
			default:
				_, _ = w.Write(chunk)
				flusher.Flush()
				time.Sleep(5 * time.Millisecond)
			}
		}
	}))
	t.Cleanup(srv.Close)

	coord := newCoordForTest(t)
	outerCtx, outerCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer outerCancel()

	writerCancel, writerDone := startServerWriter(t, coord, srv.URL+"/stream")

	var clientData []byte
	clientCtx, clientCancel := context.WithCancel(outerCtx)
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		clientData, _ = clientRead(clientCtx, coord)
	}()

	// Let data flow briefly so the writer is actively reading.
	time.Sleep(50 * time.Millisecond)

	// The only client disconnects.
	clientCancel()
	<-clientDone

	if c := atomic.LoadInt32(&coord.ClientCount); c != 0 {
		t.Errorf("ClientCount = %d after last client disconnected, want 0", c)
	}

	// HandleStream.cleanup() cancels the writer context when count hits zero.
	writerCancel()

	select {
	case <-writerDone:
		// writer exited cleanly — no goroutine leak
	case <-time.After(3 * time.Second):
		t.Fatal("writer goroutine did not exit within 3s after context cancellation")
	}

	// The client must have received actual data with the correct byte value.
	if len(clientData) == 0 {
		t.Error("client received 0 bytes — no data flowed through the coordinator")
	}
	assertAllBytes(t, "client data", clientData, streamByte)
}

// firstDiff returns the index of the first byte that differs between a and b,
// or max(len(a), len(b)) if the shorter slice is a prefix of the longer.
func firstDiff(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := range n {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}
