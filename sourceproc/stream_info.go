package sourceproc

import "m3u-stream-merger/utils/safemap"

// StreamInfo represents a stream with thread-safe operations
type StreamInfo struct {
	Title       string                                  `json:"title"`
	TvgID       string                                  `json:"tvg_id"`
	TvgChNo     string                                  `json:"tvg_ch"`
	TvgType     string                                  `json:"tvg_type"`
	LogoURL     string                                  `json:"logo"`
	Group       string                                  `json:"group"`
	URLs        *safemap.Map[string, map[string]string] `json:"-"`
	SourceM3U   string                                  `json:"source_m3u"`
	SourceIndex int                                     `json:"source_index"`
}
