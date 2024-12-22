package proxy

import (
	"context"
	"fmt"
	"m3u-stream-merger/store"
	"m3u-stream-merger/utils"
	"net/http"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

type StreamInstance struct {
	Info store.StreamInfo
	Cm   *store.ConcurrencyManager
}

func NewStreamInstance(streamUrl string, cm *store.ConcurrencyManager) (*StreamInstance, error) {
	stream, err := store.GetStreamBySlug(streamUrl)
	if err != nil {
		return nil, err
	}

	return &StreamInstance{
		Info: stream,
		Cm:   cm,
	}, nil
}

func (instance *StreamInstance) LoadBalancer(ctx context.Context, session *store.Session, method string) (*http.Response, string, string, string, error) {
	debug := os.Getenv("DEBUG") == "true"

	m3uIndexes := utils.GetM3UIndexes()

	sort.Slice(m3uIndexes, func(i, j int) bool {
		return instance.Cm.ConcurrencyPriorityValue(m3uIndexes[i], "0") > instance.Cm.ConcurrencyPriorityValue(m3uIndexes[j], "0")
	})

	maxLapsString := os.Getenv("MAX_RETRIES")
	maxLaps, err := strconv.Atoi(strings.TrimSpace(maxLapsString))
	if err != nil || maxLaps < 0 {
		maxLaps = 5
	}

	lap := 0

	// Backoff settings
	initialBackoff := 200 * time.Millisecond
	maxBackoff := 2 * time.Second
	currentBackoff := initialBackoff

	for lap < maxLaps || maxLaps == 0 {
		if debug {
			utils.SafeLogf("[DEBUG] Stream attempt %d out of %d\n", lap+1, maxLaps)
		}

		select {
		case <-ctx.Done():
			return nil, "", "", "", fmt.Errorf("Cancelling load balancer.")
		default:
			for _, index := range m3uIndexes {
				innerMap, ok := instance.Info.URLs[index]
				if !ok {
					utils.SafeLogf("Channel not found from M3U_%s: %s\n", index, instance.Info.Title)
					continue
				}

				for subIndex, url := range innerMap {
					if slices.Contains(session.TestedIndexes, index+"|"+subIndex) {
						utils.SafeLogf("Skipping M3U_%s|%s: marked as previous stream\n", index, subIndex)
						continue
					}

					if instance.Cm.CheckConcurrency(index, subIndex) {
						utils.SafeLogf("Concurrency limit reached for M3U_%s|%s: %s\n", index, subIndex, url)
						continue
					}

					resp, err := utils.CustomHttpRequest(method, url)
					if err == nil {
						if debug {
							utils.SafeLogf("[DEBUG] Successfully fetched stream from %s\n", url)
						}
						return resp, url, index, subIndex, nil
					}
					utils.SafeLogf("Error fetching stream: %s\n", err.Error())
					if debug {
						utils.SafeLogf("[DEBUG] Error fetching stream from %s: %s\n", url, err.Error())
					}
					session.SetTestedIndexes(append(session.TestedIndexes, index+"|"+subIndex))
				}
			}

			if debug {
				utils.SafeLogf("[DEBUG] All streams skipped in lap %d\n", lap)
			}
			session.SetTestedIndexes([]string{})

		}

		select {
		case <-time.After(currentBackoff):
			currentBackoff *= 2
			if currentBackoff > maxBackoff {
				currentBackoff = maxBackoff
			}
		case <-ctx.Done():
		}

		lap++
	}

	return nil, "", "", "", fmt.Errorf("Error fetching stream. Exhausted all streams.")
}
