package utils

import (
	"net/http"
	"path/filepath"
	"slices"
	"strings"
)

func EOFIsExpected(resp *http.Response) bool {
	knownMimeTypes := []string{
		"application/x-mpegurl",
		"text/plain",
		"audio/x-mpegurl",
		"audio/mpegurl",
		"application/vnd.apple.mpegurl",
	}

	knownExtensions := []string{
		".m3u",
		".m3u8",
	}

	urlPath := resp.Request.URL.Path
	extension := strings.ToLower(filepath.Ext(urlPath))

	return slices.Contains(knownMimeTypes, strings.ToLower(resp.Header.Get("Content-Type"))) || slices.Contains(knownExtensions, extension)
}
