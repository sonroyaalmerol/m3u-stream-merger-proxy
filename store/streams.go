package store

import (
	"bufio"
	"fmt"
	"m3u-stream-merger/utils"
	"os"
	"sort"
	"strings"
	"sync"
)

func GetStreamBySlug(slug string) (StreamInfo, error) {
	streamInfo, err := ParseStreamInfoBySlug(slug)
	if err != nil {
		return StreamInfo{}, fmt.Errorf("error parsing stream info: %v", err)
	}

	return *streamInfo, nil
}

var urlCache sync.Map

func ClearURLCache() {
	urlCache.Clear()
}

func GetURL(m3uIndex int, line int) string {
	cache, ok := urlCache.Load(fmt.Sprintf("%d_%d", m3uIndex, line))
	if ok {
		return cache.(string)
	}

	utils.SafeLogf("Parsing M3U #%d...\n", m3uIndex+1)
	filePath := utils.GetM3UFilePathByIndex(m3uIndex)

	file, err := os.Open(filePath)
	if err != nil {
		utils.SafeLogf("Failed to open file: %v\n", err)
		return ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	currentLine := 0

	for scanner.Scan() {
		if currentLine == line {
			url := strings.TrimSpace(scanner.Text())
			urlCache.Store(fmt.Sprintf("%d_%d", m3uIndex, line), url)
			return url
		}
		currentLine++
	}

	if err := scanner.Err(); err != nil {
		utils.SafeLogf("Error while scanning file: %v\n", err)
		return ""
	}

	utils.SafeLogf("Line %d not found in file\n", line)
	return ""
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
	for m3uIndex, urlLine := range stream.URLs {
		srcUrl := GetURL(m3uIndex, urlLine)
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
