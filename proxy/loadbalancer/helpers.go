package loadbalancer

import (
	"bufio"
	"errors"
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
	const probeBytes = 8192
	const probeTimeout = 2 * time.Second

	if resp == nil || resp.Body == nil {
		return 0, errors.New("response is empty")
	}

	br := bufio.NewReader(resp.Body)

	start := time.Now()

	type probeResult struct {
		bytes []byte
		err   error
	}
	resultCh := make(chan probeResult, 1)

	go func() {
		bytes, err := br.Peek(probeBytes)
		resultCh <- probeResult{
			bytes: bytes,
			err:   err,
		}
	}()

	select {
	case res := <-resultCh:
		elapsed := time.Since(start)
		if res.err != nil && res.err != io.EOF {
			return 0, res.err
		}
		if elapsed <= 0 {
			elapsed = time.Millisecond
		}
		throughput := float64(len(res.bytes)) / elapsed.Seconds()

		resp.Body = io.NopCloser(br)
		return throughput, nil
	case <-time.After(probeTimeout):
		return 0, fmt.Errorf("probe timed out after %v", probeTimeout)
	}
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
