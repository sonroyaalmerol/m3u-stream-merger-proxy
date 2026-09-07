package buffer

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"m3u-stream-merger/logger"
	"m3u-stream-merger/proxy"
	"m3u-stream-merger/proxy/stream/config"
	"m3u-stream-merger/store"
)

func TestResumePosition(t *testing.T) {
	c := newCoordForTest(t)
	for i := range 5 {
		c.Write(&ChunkData{Data: []byte{byte('a' + i)}, Timestamp: time.Now()})
	}

	pos, ok := c.ResumePosition(3)
	if !ok {
		t.Fatal("expected resumable position")
	}
	chunk, _ := pos.Value.(*ChunkData)
	if chunk == nil || chunk.seq != 4 {
		t.Fatalf("resume landed on seq %v, want 4", chunkSeqOrNeg(chunk))
	}

	if _, ok := c.ResumePosition(-1); !ok {
		t.Fatal("seq below everything should still resume at the oldest chunk")
	}
	if _, ok := c.ResumePosition(1000); ok {
		t.Fatal("future seq must not resume")
	}
}

func chunkSeqOrNeg(c *ChunkData) int64 {
	if c == nil {
		return -1
	}
	return c.seq
}

func TestEnsureActiveForWriterWipesErrorMarkers(t *testing.T) {
	c := newCoordForTest(t)
	c.Write(&ChunkData{Data: []byte("data1"), Timestamp: time.Now()})
	c.writeError(nil, proxy.StatusServerError)
	if atomic.LoadInt32(&c.state) == stateActive {
		t.Fatal("expected closed state after writeError")
	}

	c.EnsureActiveForWriter()
	if atomic.LoadInt32(&c.state) != stateActive {
		t.Fatal("expected active state after EnsureActiveForWriter")
	}
	if lastErr, ok := c.LastError.Load().(*ChunkData); ok && lastErr != nil {
		t.Fatal("expected LastError cleared")
	}

	found := 0
	for i := 0; i < c.config.SharedBufferSize; i++ {
		if ch, ok := c.Buffer.Value.(*ChunkData); ok && ch != nil {
			if ch.Status != 0 || ch.Error != nil {
				t.Fatal("error-marker chunk survived")
			}
			found++
		}
		c.Buffer = c.Buffer.Next()
	}
	if found != 1 {
		t.Fatalf("expected 1 data chunk, found %d", found)
	}
}

func TestReadChunksReportsRejoin(t *testing.T) {
	c := NewStreamCoordinator(t.Name(), &config.StreamConfig{
		SharedBufferSize: 4,
		ChunkSize:        512,
		TimeoutSeconds:   0,
	}, store.NewConcurrencyManager(), logger.Default)
	for range 10 {
		c.Write(&ChunkData{Data: []byte{byte('a')}, Timestamp: time.Now()})
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _, _, _, rejoined := c.ReadChunks(ctx, nil, 1)
	if !rejoined {
		t.Fatal("expected rejoined=true for a stale reader seq")
	}
	_, _, _, _, rejoined = c.ReadChunks(ctx, c.InitialPosition(), 0)
	if rejoined {
		t.Fatal("expected rejoined=false for a current reader")
	}
}
