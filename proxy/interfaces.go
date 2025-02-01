package proxy

import (
	"io"
	"m3u-stream-merger/store"
	"net/http"
)

type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type IndexProvider interface {
	GetM3UIndexes() []string
}

type SlugParser interface {
	GetStreamBySlug(slug string) (store.StreamInfo, error)
}

type ResponseWriter interface {
	http.ResponseWriter
}

type ResponseBodyReader interface {
	io.ReadCloser
}

type StreamFlusher interface {
	Flush()
}
