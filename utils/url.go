package utils

import (
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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

func SafeLogPrintf(r *http.Request, customUnsafe *string, format string, v ...any) {
	safeLogs := os.Getenv("SAFE_LOGS") == "true"
	safeString := fmt.Sprintf(format, v...)
	if safeLogs {
		if customUnsafe != nil {
			safeString = strings.ReplaceAll(safeString, *customUnsafe, "[redacted sensitive string]")
		}
		if r != nil {
			safeString = strings.ReplaceAll(safeString, r.RemoteAddr, "[redacted remote addr]")
			safeString = strings.ReplaceAll(safeString, r.URL.Path, "[redacted request url path]")
			safeString = strings.ReplaceAll(safeString, r.URL.String(), "[redacted request full url]")
		}
		urlRegex := `(https?|file):\/\/[^\s/$.?#].[^\s]*`
		re := regexp.MustCompile(urlRegex)

		safeString = re.ReplaceAllString(safeString, "[redacted url]")
	}

	log.Print(safeString)
}
