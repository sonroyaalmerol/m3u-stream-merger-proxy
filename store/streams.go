package store

import (
	"encoding/hex"
	"fmt"
	"m3u-stream-merger/config"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/utils"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/sha3"
)

func GetStreamBySlug(slug string) (StreamInfo, error) {
	streamInfo, err := ParseStreamInfoBySlug(slug)
	if err != nil {
		return StreamInfo{}, fmt.Errorf("error parsing stream info: %v", err)
	}

	return *streamInfo, nil
}

func scanSources() map[string]*StreamInfo {
	streams := make(map[string]*StreamInfo)
	var streamsMu sync.Mutex
	sessionIdHash := sha3.Sum224([]byte(time.Now().String()))
	sessionId := hex.EncodeToString(sessionIdHash[:])
	var wg sync.WaitGroup

	m3uIndexes := utils.GetM3UIndexes()
	for m3uIdx, m3uIndex := range m3uIndexes {
		wg.Add(1)
		go func(m3uIndex string, m3uPosition int) {
			defer wg.Done()
			currentIndex := 0
			err := M3UScanner(m3uIndex, sessionId, func(streamInfo *StreamInfo) {
				streamsMu.Lock()
				defer streamsMu.Unlock()

				streamInfo.SourceM3U = m3uIndex
				streamInfo.SourceIndex = currentIndex
				currentIndex++

				// Check if stream exists and update if necessary
				if existingStream, exists := streams[streamInfo.Title]; exists {
					for idx, innerMap := range streamInfo.URLs {
						if existingStream.URLs[idx] == nil {
							existingStream.URLs[idx] = make(map[string]string)
						}
						// For each URL in the inner map, add it if the hash key is not already present.
						for hashKey, url := range innerMap {
							if _, exists := existingStream.URLs[idx][hashKey]; !exists {
								existingStream.URLs[idx][hashKey] = fmt.Sprintf("%d:::%s", streamInfo.SourceIndex, url)
							}
						}
					}
				} else {
					// Store new stream if it doesn't already exist.
					streams[streamInfo.Title] = streamInfo
				}
			})
			if err != nil {
				logger.Default.Debugf("error getting streams: %v", err)
			}
		}(m3uIndex, m3uIdx)
	}
	wg.Wait()

	// Clean up old session directories
	entries, err := os.ReadDir(config.GetStreamsDirPath())
	if err == nil {
		for _, e := range entries {
			if e.Name() == sessionId {
				continue
			}
			_ = os.RemoveAll(filepath.Join(config.GetStreamsDirPath(), e.Name()))
		}
	}

	return streams
}

func GenerateStreamURL(baseUrl string, stream *StreamInfo) string {
	var subPath string
	var err error
	for _, innerMap := range stream.URLs {
		for _, srcUrl := range innerMap {
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
	}
	return fmt.Sprintf("%s/p/stream/%s", baseUrl, EncodeSlug(stream))
}

func SortStreamSubUrls(urls map[string]string) []string {
	keys := make([]string, 0, len(urls))
	for key := range urls {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		idxISplit := strings.SplitN(urls[keys[i]], ":::", 2)
		idxJSplit := strings.SplitN(urls[keys[j]], ":::", 2)
		if len(idxISplit) == 2 && len(idxJSplit) == 2 {
			idxI, err := strconv.Atoi(idxISplit[0])
			if err != nil {
				idxI = 0
			}
			idxJ, err := strconv.Atoi(idxJSplit[0])
			if err != nil {
				idxJ = 0
			}
			return idxI < idxJ
		}
		return true
	})
	return keys
}

func sortStreams(s map[string]*StreamInfo) []string {
	// Create a slice of keys
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}

	key := os.Getenv("SORTING_KEY")
	dir := strings.ToLower(os.Getenv("SORTING_DIRECTION"))

	tieBreaker := func(i, j int) bool {
		if s[keys[i]].SourceM3U != s[keys[j]].SourceM3U {
			return s[keys[i]].SourceM3U < s[keys[j]].SourceM3U
		}
		return s[keys[i]].SourceIndex < s[keys[j]].SourceIndex
	}

	// Sort the keys based on the values they point to
	switch key {
	case "tvg-id":
		sort.Slice(keys, func(i, j int) bool {
			// First compare by tvg-id
			if s[keys[i]].TvgID != s[keys[j]].TvgID {
				if dir == "desc" {
					return s[keys[i]].TvgID > s[keys[j]].TvgID
				}
				return s[keys[i]].TvgID < s[keys[j]].TvgID
			}
			// If tvg-id is same, use source ordering
			return tieBreaker(i, j)
		})
	case "tvg-chno":
		sort.Slice(keys, func(i, j int) bool {
			// First compare by tvg-chno
			if s[keys[i]].TvgChNo != s[keys[j]].TvgChNo {
				if dir == "desc" {
					return s[keys[i]].TvgChNo > s[keys[j]].TvgChNo
				}
				return s[keys[i]].TvgChNo < s[keys[j]].TvgChNo
			}
			// If tvg-chno is same, use source ordering
			return tieBreaker(i, j)
		})
	case "tvg-group":
		sort.Slice(keys, func(i, j int) bool {
			// First compare by group
			if s[keys[i]].Group != s[keys[j]].Group {
				if dir == "desc" {
					return s[keys[i]].Group > s[keys[j]].Group
				}
				return s[keys[i]].Group < s[keys[j]].Group
			}
			// If group is same, use source ordering
			return tieBreaker(i, j)
		})
	case "tvg-type":
		sort.Slice(keys, func(i, j int) bool {
			// First compare by type
			if s[keys[i]].TvgType != s[keys[j]].TvgType {
				if dir == "desc" {
					return s[keys[i]].TvgType > s[keys[j]].TvgType
				}
				return s[keys[i]].TvgType < s[keys[j]].TvgType
			}
			// If type is same, use source ordering
			return tieBreaker(i, j)
		})
	case "source":
		sort.Slice(keys, func(i, j int) bool {
			if s[keys[i]].SourceM3U != s[keys[j]].SourceM3U {
				return s[keys[i]].SourceM3U < s[keys[j]].SourceM3U
			}
			return s[keys[i]].SourceIndex < s[keys[j]].SourceIndex
		})
	default:
		sort.Slice(keys, func(i, j int) bool {
			if dir == "desc" {
				return s[keys[i]].Title > s[keys[j]].Title
			}
			return s[keys[i]].Title < s[keys[j]].Title
		})
	}

	return keys
}
