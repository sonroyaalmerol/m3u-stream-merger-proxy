package loadbalancer

import (
	"bufio"
	"bytes"
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

func evaluateBufferHealth(resp *http.Response) (float64, error) {
	const measureDuration = 2 * time.Second
	const readChunkSize = 4096

	start := time.Now()
	br := bufio.NewReader(resp.Body)

	var consumed bytes.Buffer
	totalBytes := 0

	temp := make([]byte, readChunkSize)
	deadline := time.Now().Add(measureDuration)

	for time.Now().Before(deadline) {
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

	newBody := io.MultiReader(bytes.NewReader(consumed.Bytes()), br)
	resp.Body = io.NopCloser(newBody)
	return throughput, nil
}

func contains(sl []string, val string) bool {
	for _, s := range sl {
		if s == val {
			return true
		}
	}
	return false
}

func sourceprocSortStreamSubUrls(urls map[string]string) []string {
	keys := make([]string, 0, len(urls))
	for k := range urls {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
