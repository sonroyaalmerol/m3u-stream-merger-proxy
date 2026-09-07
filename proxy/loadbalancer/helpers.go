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
	const probeReadChunk = 32 * 1024
	const defaultMaxSample = 1024 * 1024

	start := time.Now()
	originalBody := resp.Body
	br := bufio.NewReader(originalBody)

	if maxSampleBytes <= 0 {
		maxSampleBytes = defaultMaxSample
	}

	var consumed []byte
	temp := make([]byte, probeReadChunk)
	deadline := start.Add(measureWindow)

	for len(consumed) < maxSampleBytes && time.Now().Before(deadline) {
		if ctx.Err() != nil {
			break
		}
		n, err := br.Read(temp)
		if n > 0 {
			consumed = append(consumed, temp[:n]...)
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return 0, fmt.Errorf("error reading stream during measurement: %w", err)
		}
	}

	elapsed := time.Since(start)
	if elapsed <= 0 {
		elapsed = time.Millisecond
	}
	throughput := float64(len(consumed)) / elapsed.Seconds()

	// Reconstruct the body so that reads come from the buffered data followed
	// by the remaining original body, but Close() still releases the underlying
	// TCP connection.
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
