package sourceproc

import (
	"fmt"
	"hash/fnv"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"m3u-stream-merger/config"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/utils"
)

func GetStreamBySlug(slug string) (*StreamInfo, error) {
	var err error
	streamInfo, err := ParseStreamInfoBySlug(slug)
	if err != nil {
		return &StreamInfo{}, fmt.Errorf("error parsing stream info: %v", err)
	}

	return streamInfo, nil
}

func GenerateStreamURL(baseUrl string, stream *StreamInfo) string {
	subPaths := make(chan string, len(stream.URLs))
	var wg sync.WaitGroup

	// Process URLs concurrently
	for _, innerMap := range stream.URLs {
		for _, srcUrl := range innerMap {
			wg.Add(1)
			go func(url string) {
				defer wg.Done()
				if subPath, err := utils.GetSubPathFromUrl(url); err == nil {
					subPaths <- subPath
				}
			}(srcUrl)
		}
	}

	// Close channel after all goroutines complete
	go func() {
		wg.Wait()
		close(subPaths)
	}()

	// Use the first valid subPath
	for subPath := range subPaths {
		return fmt.Sprintf("%s/p/%s/%s", baseUrl, subPath, EncodeSlug(stream))
	}

	// Fallback to default path
	return fmt.Sprintf("%s/p/stream/%s", baseUrl, EncodeSlug(stream))
}

func SortStreamSubUrls(urls map[string]string) []string {
	type urlInfo struct {
		key string
		idx int
	}

	urlInfos := make([]urlInfo, 0, len(urls))
	for key, url := range urls {
		idxStr := strings.SplitN(url, ":::", 2)[0]
		idx, _ := strconv.Atoi(idxStr)
		urlInfos = append(urlInfos, urlInfo{key, idx})
	}

	sort.Slice(urlInfos, func(i, j int) bool {
		return urlInfos[i].idx < urlInfos[j].idx
	})

	result := make([]string, len(urlInfos))
	for i, info := range urlInfos {
		result[i] = info.key
	}
	return result
}

func ClearProcessedM3Us() {
	err := os.RemoveAll(config.GetProcessedDirPath())
	if err != nil {
		logger.Default.Error(err.Error())
	}
}

func fnvHash(s string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(s))
	return h.Sum32()
}
