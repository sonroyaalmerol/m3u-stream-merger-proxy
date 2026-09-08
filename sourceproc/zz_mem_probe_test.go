package sourceproc

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"testing"
	"time"
)

func procStatus(t testing.TB, field string) uint64 {
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		if !strings.HasPrefix(line, field) {
			continue
		}
		fields := strings.Fields(line)
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return kb * 1024
	}
	return 0
}

func TestMemProbeIngest(t *testing.T) {
	countEnv := os.Getenv("MEM_PROBE_STREAMS")
	if countEnv == "" {
		t.Skip("set MEM_PROBE_STREAMS")
	}
	count, err := strconv.Atoi(countEnv)
	if err != nil {
		t.Fatal(err)
	}

	cleanup := setupTestEnvironment(t)
	defer cleanup()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		bw := make([]byte, 0, 512)
		_, _ = w.Write([]byte("#EXTM3U\n"))
		for i := range count {
			bw = bw[:0]
			bw = fmt.Appendf(bw, "#EXTINF:-1 tvg-id=\"chan.%d.tv\" tvg-name=\"Some Channel Name HD %d\" tvg-logo=\"http://example.com/logo/channel.png\" tvg-chno=\"%d\" group-title=\"Sports | International %d\",Some Channel Name HD %d\nhttp://example.com/live/user/pass/%d.ts\n", i, i, i, i%500, i, i)
			if _, err := w.Write(bw); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	t.Setenv("M3U_URL_1", server.URL)
	_ = os.Unsetenv("M3U_URL_2")
	_ = os.Unsetenv("M3U_URL_3")

	var peakAnon, peakFile uint64
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-time.After(20 * time.Millisecond):
				anon := procStatus(t, "RssAnon:")
				threshold, _ := strconv.ParseUint(os.Getenv("MEM_PROBE_HEAP_MIB"), 10, 64)
				if anon > peakAnon && anon > threshold<<20 && os.Getenv("MEM_PROBE_HEAP") != "" {
					f, err := os.Create(os.Getenv("MEM_PROBE_HEAP"))
					if err == nil {
						_ = pprof.WriteHeapProfile(f)
						_ = f.Close()
					}
				}
				peakAnon = max(peakAnon, anon)
				peakFile = max(peakFile, procStatus(t, "RssFile:"))
			}
		}
	}()

	start := time.Now()
	processor := NewProcessor()
	if err := processor.Run(context.Background(), httptest.NewRequest(http.MethodGet, "http://example.com", nil)); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	close(done)

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	mib := func(v uint64) float64 { return float64(v) / (1 << 20) }
	var storeBytes int64
	entries, _ := os.ReadDir(storeDir())
	for _, entry := range entries {
		if info, err := entry.Info(); err == nil {
			storeBytes += info.Size()
		}
	}
	t.Logf("streams=%d elapsed=%s peakRSS=%.1f peakAnon=%.1f peakFile=%.1f heapInuse=%.1f sys=%.1f store=%.1f (MiB) accepted=%d",
		count, elapsed.Round(time.Second),
		mib(procStatus(t, "VmHWM:")), mib(peakAnon), mib(peakFile),
		mib(ms.HeapInuse), mib(ms.Sys), float64(storeBytes)/(1<<20),
		processor.streamCount.Load())
}
