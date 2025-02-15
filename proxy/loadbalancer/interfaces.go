package loadbalancer

import (
	"m3u-stream-merger/sourceproc"
	"net/http"
)

type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type IndexProvider interface {
	GetM3UIndexes() []string
}

type SlugParser interface {
	GetStreamBySlug(slug string) (*sourceproc.StreamInfo, error)
}
