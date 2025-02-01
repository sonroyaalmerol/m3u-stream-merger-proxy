package loadbalancer

import (
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
