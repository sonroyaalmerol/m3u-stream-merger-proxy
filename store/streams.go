package store

import (
	"fmt"
	"m3u-stream-merger/utils"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
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

	sort.Slice(result, func(i, j int) bool {
		return calculateSortScore(result[i]) < calculateSortScore(result[j])
	})

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

func getSortingValue(s StreamInfo) string {
	key := os.Getenv("SORTING_KEY")

	var value string
	switch key {
	case "tvg-id":
		value = s.TvgID
	case "tvg-chno":
		value = s.TvgChNo
	default:
		value = s.TvgID
	}

	// Try to parse the value as a float.
	if numValue, err := strconv.ParseFloat(value, 64); err == nil {
		return fmt.Sprintf("%010.2f", numValue) + s.Title
	}

	// If parsing fails, fall back to using the original string value.
	return value + s.Title
}

func calculateSortScore(s StreamInfo) float64 {
	// Add to the sorted set with tvg_id as the score
	maxLen := 40
	base := float64(256)

	// Normalize length by padding the string
	paddedString := strings.ToLower(getSortingValue(s))
	if len(paddedString) < maxLen {
		paddedString = paddedString + strings.Repeat("\x00", maxLen-len(paddedString))
	}

	sortScore := 0.0
	for i := 0; i < len(paddedString); i++ {
		charValue := float64(paddedString[i])
		sortScore += charValue / math.Pow(base, float64(i+1))
	}

	return sortScore
}
