package utils

import (
	"net/http"
	"net/url"
	"testing"
)

func benchResponse(contentType, path string) *http.Response {
	u, _ := url.Parse("http://example.com" + path)
	return &http.Response{
		Header:  http.Header{"Content-Type": []string{contentType}},
		Request: &http.Request{URL: u},
	}
}

// BenchmarkIsAnM3U8Media runs once per streamed chunk in the buffered path.
func BenchmarkIsAnM3U8Media(b *testing.B) {
	cases := []struct {
		name string
		resp *http.Response
	}{
		{"ts_miss", benchResponse("video/mp2t", "/live/1234.ts")},
		{"m3u8_mime", benchResponse("application/x-mpegURL", "/live/1234")},
		{"m3u8_ext", benchResponse("video/mp2t", "/live/1234.m3u8")},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = IsAnM3U8Media(tc.resp)
			}
		})
	}
}

// BenchmarkGetEnv runs per upstream request and per health-check candidate.
func BenchmarkGetEnv(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = GetEnv("USER_AGENT")
		_ = GetEnv("HTTP_ACCEPT")
	}
}
