package sourceproc

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"m3u-stream-merger/config"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/utils"

	"github.com/puzpuzpuz/xsync/v3"
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
	if stream.URLs == nil {
		stream.URLs = xsync.NewMapOf[string, map[string]string]()
	}

	extension := ""
	finalUrl := ""

	stream.URLs.Range(func(_ string, innerMap map[string]string) bool {
		for _, srcUrl := range innerMap {
			if extension == "" {
				if ext, err := utils.GetFileExtensionFromUrl(srcUrl); err == nil {
					extension = ext
				}
			}
			if finalUrl == "" {
				if subPath, err := utils.GetSubPathFromUrl(srcUrl); err == nil {
					finalUrl = fmt.Sprintf("%s/p/%s/%s", baseUrl, subPath, EncodeSlug(stream))
				}
			}
			if finalUrl != "" && extension != "" {
				return false
			}
		}
		return true
	})

	if finalUrl == "" {
		finalUrl = fmt.Sprintf("%s/p/stream/%s", baseUrl, EncodeSlug(stream))
	}

	if strings.Contains(extension, ".m3u") {
		extension = ""
	}

	return finalUrl + extension
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
