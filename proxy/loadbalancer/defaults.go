package loadbalancer

import (
	sourceproc "m3u-stream-merger/source_processor"
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

func (p *DefaultSlugParser) GetStreamBySlug(slug string) (*sourceproc.StreamInfo, error) {
	return sourceproc.GetStreamBySlug(slug)
}
