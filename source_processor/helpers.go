package sourceproc

import (
	"fmt"
	"sync"

	"m3u-stream-merger/utils"
)

func GetStreamBySlug(slug string) (*StreamInfo, error) {
	streamInfo, ok := M3uCache.slugToInfoCache.Load(slug)
	if !ok {
		var err error
		streamInfo, err = ParseStreamInfoBySlug(slug)
		if err != nil {
			return &StreamInfo{}, fmt.Errorf("error parsing stream info: %v", err)
		}

		M3uCache.slugToInfoCache.Store(slug, streamInfo)
	}

	return streamInfo.(*StreamInfo), nil
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
