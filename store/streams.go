package store

import (
	"fmt"
	"m3u-stream-merger/utils"
	"os"
	"sort"
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
		streams = make(map[string]StreamInfo) // Map to store unique streams by title
		result  = make([]StreamInfo, 0)       // Slice to store final results
	)

	for _, m3uIndex := range utils.GetM3UIndexes() {
		err := M3UScanner(m3uIndex, func(streamInfo StreamInfo) {
			// Check uniqueness and update if necessary
			if existingStream, exists := streams[streamInfo.Title]; exists {
				for idx, url := range streamInfo.URLs {
					existingStream.URLs[idx] = url
				}
				streams[streamInfo.Title] = existingStream
			} else {
				streams[streamInfo.Title] = streamInfo
			}
		})
		if err != nil && debug {
			utils.SafeLogf("error getting streams: %v\n", err)
		}
	}

	for _, stream := range streams {
		result = append(result, stream)
	}

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
			return fmt.Sprintf("%s/proxy/%s/%s", baseUrl, subPath, stream.Slug)
		}

		return fmt.Sprintf("%s/proxy/%s/%s%s", baseUrl, subPath, stream.Slug, ext)
	}
	return fmt.Sprintf("%s/proxy/stream/%s", baseUrl, stream.Slug)
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
