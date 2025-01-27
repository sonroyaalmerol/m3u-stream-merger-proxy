package store

import (
	"encoding/hex"
	"fmt"
	"m3u-stream-merger/utils"
	"os"
	"path/filepath"
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

func GetStreams() []StreamInfo {
	var (
		debug   = os.Getenv("DEBUG") == "true"
		result  = make([]StreamInfo, 0) // Slice to store final results
		streams sync.Map
	)

	sessionIdHash := sha3.Sum224([]byte(time.Now().String()))
	sessionId := hex.EncodeToString(sessionIdHash[:])

	var wg sync.WaitGroup
	for _, m3uIndex := range utils.GetM3UIndexes() {
		wg.Add(1)
		go func(m3uIndex string) {
			defer wg.Done()

			err := M3UScanner(m3uIndex, sessionId, func(streamInfo StreamInfo) {
				// Check uniqueness and update if necessary
				if existingStream, exists := streams.Load(streamInfo.Title); exists {
					for idx, innerMap := range streamInfo.URLs {
						if _, ok := existingStream.(StreamInfo).URLs[idx]; !ok {
							existingStream.(StreamInfo).URLs[idx] = innerMap
							continue
						}

						for subIdx, url := range innerMap {
							existingStream.(StreamInfo).URLs[idx][subIdx] = url
						}
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

	entries, err := os.ReadDir(streamsDirPath)
	if err == nil {
		for _, e := range entries {
			if e.Name() == sessionId {
				continue
			}

			_ = os.RemoveAll(filepath.Join(streamsDirPath, e.Name()))
		}
	}

	streams.Range(func(key, value any) bool {
		stream := value.(StreamInfo)
		result = append(result, stream)
		return true
	})

	SortStreams(result)

	return result
}

func GenerateStreamURL(baseUrl string, stream StreamInfo) string {
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
