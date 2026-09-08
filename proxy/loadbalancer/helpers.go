package loadbalancer

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"
)

const maxHealthSampleBytes = 1024 * 1024

type streamTestResult struct {
	result *LoadBalancerResult
	health float64
	err    error
}

// readCloser combines a Reader and a Closer so that closing releases the
// underlying transport connection while reading comes from a different source.
type readCloser struct {
	io.Reader
	io.Closer
}

func evaluateBufferHealth(ctx context.Context, resp *http.Response, maxSampleBytes int) (float64, error) {
	const measureWindow = 2 * time.Second
	const stallWindow = 2 * measureWindow
	const probeReadChunk = 32 * 1024

	start := time.Now()
	originalBody := resp.Body
	br := bufio.NewReader(originalBody)

	stopOnCancel := context.AfterFunc(ctx, func() { _ = originalBody.Close() })
	defer stopOnCancel()

	stalled := make(chan struct{})
	stallTimer := time.AfterFunc(stallWindow, func() {
		close(stalled)
		_ = originalBody.Close()
	})
	defer stallTimer.Stop()

	if maxSampleBytes <= 0 || maxSampleBytes > maxHealthSampleBytes {
		maxSampleBytes = maxHealthSampleBytes
	}

	consumed := make([]byte, maxSampleBytes)
	consumedBytes := 0
	deadline := start.Add(measureWindow)

	for consumedBytes < maxSampleBytes && time.Now().Before(deadline) {
		if ctx.Err() != nil {
			break
		}
		readEnd := min(consumedBytes+probeReadChunk, maxSampleBytes)
		n, err := br.Read(consumed[consumedBytes:readEnd])
		consumedBytes += n
		if err != nil {
			if err == io.EOF {
				break
			}
			return 0, fmt.Errorf("error reading stream during measurement: %w", err)
		}
	}
	consumed = consumed[:consumedBytes]

	if !stallTimer.Stop() {
		<-stalled
		return 0, fmt.Errorf("stream stalled during health measurement after %s", stallWindow)
	}

	elapsed := time.Since(start)
	if elapsed <= 0 {
		elapsed = time.Millisecond
	}
	throughput := float64(consumedBytes) / elapsed.Seconds()

	newBody := io.MultiReader(bytes.NewReader(consumed), br)
	resp.Body = readCloser{Reader: newBody, Closer: originalBody}
	return throughput, nil
}

// isAcceptableStreamStatus returns true for HTTP status codes that indicate a
// usable stream body.  Besides the standard 200 OK, 206 Partial Content is
// included because many IPTV servers return it even without a Range header.
func isAcceptableStreamStatus(code int) bool {
	switch code {
	case http.StatusOK, http.StatusPartialContent:
		return true
	default:
		return false
	}
}

func sourceprocSortStreamSubUrls(urls map[string]string) []string {
	keys := make([]string, 0, len(urls))
	for k := range urls {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
