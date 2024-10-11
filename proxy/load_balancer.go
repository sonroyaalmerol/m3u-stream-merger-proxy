package proxy

import (
	"fmt"
	"m3u-stream-merger/utils"
	"net/http"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
)

func (instance *StreamInstance) LoadBalancer(previous *[]int, method string) (*http.Response, string, int, error) {
	debug := os.Getenv("DEBUG") == "true"

	m3uIndexes := utils.GetM3UIndexes()

	sort.Slice(m3uIndexes, func(i, j int) bool {
		return instance.Database.ConcurrencyPriorityValue(i) > instance.Database.ConcurrencyPriorityValue(j)
	})

	maxLapsString := os.Getenv("MAX_RETRIES")
	maxLaps, err := strconv.Atoi(strings.TrimSpace(maxLapsString))
	if err != nil || maxLaps < 0 {
		maxLaps = 5
	}

	lap := 0

	for lap < maxLaps || maxLaps == 0 {
		if debug {
			utils.SafeLogf("[DEBUG] Stream attempt %d out of %d\n", lap+1, maxLaps)
		}
		allSkipped := true // Assume all URLs might be skipped

		for _, index := range m3uIndexes {
			if slices.Contains(*previous, index) {
				utils.SafeLogf("Skipping M3U_%d: marked as previous stream\n", index+1)
				continue
			}

			url, ok := instance.Info.URLs[index]
			if !ok {
				utils.SafeLogf("Channel not found from M3U_%d: %s\n", index+1, instance.Info.Title)
				continue
			}

			if instance.Database.CheckConcurrency(index) {
				utils.SafeLogf("Concurrency limit reached for M3U_%d: %s\n", index+1, url)
				continue
			}

			allSkipped = false // At least one URL is not skipped

			resp, err := utils.CustomHttpRequest(method, url)
			if err == nil {
				if debug {
					utils.SafeLogf("[DEBUG] Successfully fetched stream from %s\n", url)
				}
				return resp, url, index, nil
			}
			utils.SafeLogf("Error fetching stream: %s\n", err.Error())
			if debug {
				utils.SafeLogf("[DEBUG] Error fetching stream from %s: %s\n", url, err.Error())
			}
		}

		if allSkipped {
			if debug {
				utils.SafeLogf("[DEBUG] All streams skipped in lap %d\n", lap)
			}
			*previous = []int{}
		}

		lap++
	}

	return nil, "", -1, fmt.Errorf("Error fetching stream. Exhausted all streams.")
}
