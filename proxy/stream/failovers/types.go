package failovers

import "sync"

type M3U8Segment struct {
	sync.RWMutex
	URL       string `json:"url"`
	SourceM3U string `json:"source_m3u"`
}
