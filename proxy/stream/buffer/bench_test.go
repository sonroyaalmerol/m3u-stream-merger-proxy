package buffer

import (
	"context"
	"io"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/proxy/stream/config"
	"m3u-stream-merger/store"
	"sync"
	"testing"
	"time"
)

func benchConfig() *config.StreamConfig {
	return &config.StreamConfig{
		SharedBufferSize:   8,
		ChunkSize:          1024 * 1024,
		TimeoutSeconds:     3,
		InitialBackoff:     200 * time.Millisecond,
		MaxRetries:         5,
		ExpectedThroughput: 0,
	}
}

type nopLogger struct{}

func (nopLogger) Log(string)                  {}
func (nopLogger) Logf(string, ...any)         {}
func (nopLogger) Debug(string)                {}
func (nopLogger) Debugf(string, ...any)       {}
func (nopLogger) Warn(string)                 {}
func (nopLogger) Warnf(string, ...any)        {}
func (nopLogger) Error(string)                {}
func (nopLogger) Errorf(string, ...any)       {}
func (nopLogger) Fatal(string)                {}
func (nopLogger) Fatalf(string, ...any)       {}
func (nopLogger) SetLogFile(string) error     { return nil }
func (nopLogger) GetLogFile() (string, error) { return "", nil }

var _ logger.Logger = nopLogger{}

func newBenchCoordinator(b *testing.B) *StreamCoordinator {
	b.Helper()
	return NewStreamCoordinator("bench", benchConfig(), store.NewConcurrencyManager(), nopLogger{})
}

// BenchmarkWrite covers the per-chunk lock + ring advance + notify path.
func BenchmarkWrite(b *testing.B) {
	c := newBenchCoordinator(b)
	_ = c.RegisterClient()
	data := make([]byte, 64*1024)

	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Write(&ChunkData{Data: data, Timestamp: time.Now()})
	}
}

// BenchmarkWriteWithReaders covers write cost under realistic reader contention.
func BenchmarkWriteWithReaders(b *testing.B) {
	for _, readers := range []int{1, 4, 16} {
		b.Run(readerName(readers), func(b *testing.B) {
			c := newBenchCoordinator(b)
			_ = c.RegisterClient()
			ctx, cancel := context.WithCancel(context.Background())
			var wg sync.WaitGroup
			for range readers {
				wg.Go(func() {
					pos := c.InitialPosition()
					var seq int64
					for ctx.Err() == nil {
						_, _, pos, seq, _ = c.ReadChunks(ctx, pos, seq)
					}
				})
			}

			data := make([]byte, 64*1024)
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				c.Write(&ChunkData{Data: data, Timestamp: time.Now()})
			}
			b.StopTimer()
			cancel()
			wg.Wait()
		})
	}
}

func readerName(n int) string {
	switch n {
	case 1:
		return "readers=1"
	case 4:
		return "readers=4"
	default:
		return "readers=16"
	}
}

// BenchmarkReadChunksDrain covers draining a full ring, including result allocs.
func BenchmarkReadChunksDrain(b *testing.B) {
	c := newBenchCoordinator(b)
	_ = c.RegisterClient()
	data := make([]byte, 64*1024)
	for i := 0; i < benchConfig().SharedBufferSize-1; i++ {
		c.Write(&ChunkData{Data: data, Timestamp: time.Now()})
	}

	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pos := c.InitialPosition().Next().Next()
		_, _, _, _, _ = c.ReadChunks(ctx, pos, 0)
	}
}

type slowReader struct {
	chunk     []byte
	remaining int
}

func (r *slowReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	r.remaining--
	return copy(p, r.chunk), nil
}

func (r *slowReader) Close() error { return nil }

// BenchmarkReadAndWriteStream covers the whole media-writer chunk pipeline.
func BenchmarkReadAndWriteStream(b *testing.B) {
	chunkSize := 256 * 1024
	cfg := benchConfig()
	cfg.ChunkSize = chunkSize

	b.ReportAllocs()
	b.SetBytes(int64(chunkSize))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		c := NewStreamCoordinator("bench", cfg, store.NewConcurrencyManager(), nopLogger{})
		_ = c.RegisterClient()
		body := &slowReader{chunk: make([]byte, chunkSize), remaining: 64}
		b.StartTimer()

		_ = c.readAndWriteStream(context.Background(), body, c.writeChunk)
	}
}

// BenchmarkShortReads models a real socket, where reads are far under ChunkSize.
func BenchmarkShortReads(b *testing.B) {
	cfg := benchConfig()
	cfg.ChunkSize = 1024 * 1024
	readSize := 32 * 1024

	b.ReportAllocs()
	b.SetBytes(int64(readSize * 63))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		c := NewStreamCoordinator("bench", cfg, store.NewConcurrencyManager(), nopLogger{})
		_ = c.RegisterClient()
		body := &slowReader{chunk: make([]byte, readSize), remaining: 63}
		b.StartTimer()

		_ = c.readAndWriteStream(context.Background(), body, c.writeChunk)
	}
}

func BenchmarkParsePlaylist(b *testing.B) {
	c := newBenchCoordinator(b)
	var sb []byte
	sb = append(sb, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:6\n#EXT-X-MEDIA-SEQUENCE:1234\n"...)
	for range 64 {
		sb = append(sb, "#EXTINF:6.0,\nsegment-000000000.ts\n"...)
	}
	content := string(sb)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.parsePlaylist("http://example.com/live/index.m3u8", content); err != nil {
			b.Fatal(err)
		}
	}
}
