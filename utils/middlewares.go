package utils

import (
	"encoding/base64"
	"m3u-stream-merger/logger"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"unsafe"
)

func GeneralParser(value string) string {
	if strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
		value = strings.Trim(value, `"`)
	}

	return value
}

type compiledFilter struct {
	src string
	re  *regexp.Regexp
}

var titleFilterCache atomic.Pointer[compiledFilter]

func TvgNameParser(value string) string {
	substrFilter := os.Getenv("TITLE_SUBSTR_FILTER")
	if substrFilter != "" {
		if c := titleFilterCache.Load(); c == nil || c.src != substrFilter {
			re, err := regexp.Compile(substrFilter)
			if err != nil {
				logger.Default.Errorf("Error compiling character filter regex: %v", err)
			}
			titleFilterCache.Store(&compiledFilter{src: substrFilter, re: re})
		}
		if c := titleFilterCache.Load(); c != nil && c.re != nil {
			value = c.re.ReplaceAllString(value, "")
		}
	}

	return GeneralParser(value)
}

func TvgIdParser(value string) string {
	return GeneralParser(value)
}

func TvgChNoParser(value string) string {
	return GeneralParser(value)
}

func TvgTypeParser(value string) string {
	return GeneralParser(value)
}

func GroupTitleParser(value string) string {
	return GeneralParser(value)
}

type envValue struct {
	src string
	val string
	ok  bool
}

var baseURLCache atomic.Pointer[envValue]

// cachedBaseURL memoizes BASE_URL per distinct value; ok mirrors url.Parse success for JoinPath parity.
func cachedBaseURL() (string, bool) {
	src := os.Getenv("BASE_URL")
	c := baseURLCache.Load()
	if c == nil || c.src != src {
		_, perr := url.Parse(src)
		c = &envValue{src: src, val: strings.TrimSuffix(src, "/"), ok: perr == nil}
		baseURLCache.Store(c)
	}
	return c.val, c.ok
}

// isHTTPAbsURL mirrors url.Parse's http/https scheme plus non-empty host check without parsing.
func isHTTPAbsURL(value string) bool {
	var rest string
	switch {
	case len(value) >= 7 && strings.EqualFold(value[:7], "http://"):
		rest = value[7:]
	case len(value) >= 8 && strings.EqualFold(value[:8], "https://"):
		rest = value[8:]
	default:
		return false
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	return rest != ""
}

func TvgLogoParser(value string) string {
	value = GeneralParser(value)

	if !isHTTPAbsURL(value) {
		return value
	}
	base, ok := cachedBaseURL()
	if !ok {
		return value
	}

	const sep = "/a/"
	enc := base64.URLEncoding
	buf := make([]byte, len(base)+len(sep)+enc.EncodedLen(len(value)))
	n := copy(buf, base)
	n += copy(buf[n:], sep)
	enc.Encode(buf[n:], unsafe.Slice(unsafe.StringData(value), len(value)))

	// buf is local and never written again, so the view is immutable.
	return unsafe.String(unsafe.SliceData(buf), len(buf))
}
