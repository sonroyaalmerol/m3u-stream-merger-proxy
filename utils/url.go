package utils

import (
	"encoding/base64"
	"strings"
)

func GetStreamUrl(slug string) string {
	return base64.URLEncoding.EncodeToString([]byte(slug))
}

func GetStreamSlugFromUrl(streamUID string) string {
	decoded, err := base64.URLEncoding.DecodeString(streamUID)
	if err != nil {
		return ""
	}
	return string(decoded)
}

func IsPlaylistFile(url string) bool {
	urlClean := strings.TrimSpace(strings.ToLower(url))

	return strings.HasSuffix(urlClean, ".m3u") || strings.HasSuffix(urlClean, ".m3u8")
}
