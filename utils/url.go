package utils

import (
	"net/http"
	"path/filepath"
	"slices"
	"strings"
)

func IsAnM3U8Media(resp *http.Response) bool {
	knownMimeTypes := []string{
		"application/x-mpegurl",
		"text/plain",
		"audio/x-mpegurl",
		"audio/mpegurl",
		"application/vnd.apple.mpegurl",
	}

	conditionTwo := false
	if resp.Request != nil && resp.Request.URL != nil {
		urlPath := resp.Request.URL.Path
		knownExtensions := []string{
			".m3u",
			".m3u8",
		}

		extension := strings.ToLower(filepath.Ext(urlPath))
		conditionTwo = slices.Contains(knownExtensions, extension)
	}

	return slices.Contains(knownMimeTypes, strings.ToLower(resp.Header.Get("Content-Type"))) || conditionTwo
}
