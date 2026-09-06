package stream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"m3u-stream-merger/proxy/client"
	"m3u-stream-merger/proxy/loadbalancer"
	"m3u-stream-merger/proxy/stream/buffer"
	"m3u-stream-merger/proxy/stream/config"
	"m3u-stream-merger/store"
)

type quietLogger struct{}

func (quietLogger) Log(string)                  {}
func (quietLogger) Logf(string, ...any)         {}
func (quietLogger) Debug(string)                {}
func (quietLogger) Debugf(string, ...any)       {}
func (quietLogger) Warn(string)                 {}
func (quietLogger) Warnf(string, ...any)        {}
func (quietLogger) Error(string)                {}
func (quietLogger) Errorf(string, ...any)       {}
func (quietLogger) Fatal(string)                {}
func (quietLogger) Fatalf(string, ...any)       {}
func (quietLogger) SetLogFile(string) error     { return nil }
func (quietLogger) GetLogFile() (string, error) { return "", nil }

const benchChunks = 512

type discardWriter struct{ h http.Header }

func (d *discardWriter) Header() http.Header {
	if d.h == nil {
		d.h = make(http.Header)
	}
	return d.h
}
func (d *discardWriter) Write(p []byte) (int, error) { return len(p), nil }
func (d *discardWriter) WriteHeader(int)             {}
func (d *discardWriter) Flush()                      {}

// BenchmarkHandleStreamChunkLoop covers the per-chunk loop each client runs.
func BenchmarkHandleStreamChunkLoop(b *testing.B) {
	cases := []struct {
		name        string
		contentType string
		path        string
	}{
		{"direct_ts", "video/mp2t", "/live/1234.ts"},
		{"direct_ts_mixedcase", "video/MP2T", "/live/1234.ts"},
		{"hls_ext", "video/mp2t", "/live/1234.m3u8"},
	}

	payload := make([]byte, 32*1024)
	b.SetBytes(int64(len(payload) * benchChunks))

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(http.StatusOK)
				for range benchChunks {
					select {
					case <-r.Context().Done():
						return
					default:
					}
					if _, err := w.Write(payload); err != nil {
						return
					}
					w.(http.Flusher).Flush()
				}
			}))
			defer srv.Close()

			cfg := &config.StreamConfig{
				SharedBufferSize: 8,
				ChunkSize:        32 * 1024,
				TimeoutSeconds:   1,
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				coord := buffer.NewStreamCoordinator("bench", cfg, store.NewConcurrencyManager(), quietLogger{})
				handler := NewStreamHandler(cfg, coord, quietLogger{})

				ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
				req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+tc.path, nil)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					b.Fatal(err)
				}
				lbRes := &loadbalancer.LoadBalancerResult{Response: resp, Index: "1", URL: srv.URL}
				sc := client.NewStreamClient(&discardWriter{}, req)
				b.StartTimer()

				_ = handler.HandleStream(ctx, lbRes, sc)
				cancel()
			}
		})
	}
}
