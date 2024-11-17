package store

import (
	"fmt"
	"m3u-stream-merger/utils"
	"os"
	"sort"
	"sync"
)

func GetStreamBySlug(slug string) (StreamInfo, error) {
	streamInfo, err := ParseStreamInfoBySlug(slug)
	if err != nil {
		return StreamInfo{}, fmt.Errorf("error parsing stream info: %v", err)
	}

	return *streamInfo, nil
}

func GetStreams() []StreamInfo {
	var (
		debug   = os.Getenv("DEBUG") == "true"
		result  = make([]StreamInfo, 0) // Slice to store final results
		streams sync.Map
	)

	var wg sync.WaitGroup
	for _, m3uIndex := range utils.GetM3UIndexes() {
		wg.Add(1)
		go func(m3uIndex int) {
			defer wg.Done()

			err := M3UScanner(m3uIndex, func(streamInfo StreamInfo) {
				// Check uniqueness and update if necessary
				if existingStream, exists := streams.Load(streamInfo.Title); exists {
					for idx, url := range streamInfo.URLs {
						existingStream.(StreamInfo).URLs[idx] = url
					}
					streams.Store(streamInfo.Title, existingStream)
				} else {
					streams.Store(streamInfo.Title, streamInfo)
				}
			})
			if err != nil && debug {
				utils.SafeLogf("error getting streams: %v\n", err)
			}
		}(m3uIndex)
	}
	wg.Wait()

	streams.Range(func(key, value any) bool {
		stream := value.(StreamInfo)
		result = append(result, stream)
		return true
	})

	sortStreams(result)

	return result
}

func GenerateStreamURL(baseUrl string, stream StreamInfo) string {
	var subPath string
	var err error
	for _, srcUrl := range stream.URLs {
		subPath, err = utils.GetSubPathFromUrl(srcUrl)
		if err != nil {
			continue
		}

		ext, err := utils.GetFileExtensionFromUrl(srcUrl)
		if err != nil {
			return fmt.Sprintf("%s/p/%s/%s", baseUrl, subPath, EncodeSlug(stream))
		}

		return fmt.Sprintf("%s/p/%s/%s%s", baseUrl, subPath, EncodeSlug(stream), ext)
	}
	return fmt.Sprintf("%s/p/stream/%s", baseUrl, EncodeSlug(stream))
}

func sortStreams(s []StreamInfo) {
	key := os.Getenv("SORTING_KEY")

	switch key {
	case "tvg-id":
		sort.Slice(s, func(i, j int) bool {
			return s[i].TvgID < s[j].TvgID
		})
	case "tvg-chno":
		sort.Slice(s, func(i, j int) bool {
			return s[i].TvgChNo < s[j].TvgChNo
		})
	default:
		sort.Slice(s, func(i, j int) bool {
			return s[i].Title < s[j].Title
		})
	}
}
