package proxy

import (
	"m3u-stream-merger/store"
	"m3u-stream-merger/utils"
)

type DefaultIndexProvider struct {
	IndexProvider
}

func (p *DefaultIndexProvider) GetM3UIndexes() []string {
	return utils.GetM3UIndexes()
}

type DefaultSlugParser struct {
	SlugParser
}

func (p *DefaultSlugParser) GetStreamBySlug(slug string) (store.StreamInfo, error) {
	return store.GetStreamBySlug(slug)
}
