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

func evaluateBufferHealth(ctx context.Context, resp *http.Response, readChunkSize int) (float64, error) {
	const measureDuration = 2 * time.Second

	start := time.Now()
	originalBody := resp.Body
	br := bufio.NewReader(originalBody)

	var consumed bytes.Buffer
	totalBytes := 0

	temp := make([]byte, readChunkSize)
	deadline := time.Now().Add(measureDuration)

	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			break
		}
		n, err := br.Read(temp)
		if n > 0 {
			totalBytes += n
			consumed.Write(temp[:n])
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return 0, fmt.Errorf("error reading stream during measurement: %w", err)
		}
	}

	elapsed := time.Since(start)
	if elapsed.Seconds() == 0 {
		elapsed = time.Millisecond
	}
	throughput := float64(totalBytes) / elapsed.Seconds()

	// Reconstruct the body so that reads come from the buffered data followed
	// by the remaining original body, but Close() still releases the underlying
	// TCP connection.
	newBody := io.MultiReader(bytes.NewReader(consumed.Bytes()), br)
	resp.Body = readCloser{Reader: newBody, Closer: originalBody}
	return throughput, nil
}

func sourceprocSortStreamSubUrls(urls map[string]string) []string {
	keys := make([]string, 0, len(urls))
	for k := range urls {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
