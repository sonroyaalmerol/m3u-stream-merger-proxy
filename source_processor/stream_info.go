package sourceproc

import (
	"sync"
)

// StreamInfo represents a stream with thread-safe operations
type StreamInfo struct {
	sync.RWMutex
	Title       string                       `json:"title"`
	TvgID       string                       `json:"tvg_id"`
	TvgChNo     string                       `json:"tvg_ch"`
	TvgType     string                       `json:"tvg_type"`
	LogoURL     string                       `json:"logo"`
	Group       string                       `json:"group"`
	URLs        map[string]map[string]string `json:"-"`
	SourceM3U   string                       `json:"source_m3u"`
	SourceIndex int                          `json:"source_index"`
}

// Clone creates a deep copy of StreamInfo
func (s *StreamInfo) Clone() *StreamInfo {
	s.RLock()
	defer s.RUnlock()

	clone := &StreamInfo{
		Title:       s.Title,
		TvgID:       s.TvgID,
		TvgChNo:     s.TvgChNo,
		TvgType:     s.TvgType,
		LogoURL:     s.LogoURL,
		Group:       s.Group,
		SourceM3U:   s.SourceM3U,
		SourceIndex: s.SourceIndex,
		URLs:        make(map[string]map[string]string),
	}

	for k, v := range s.URLs {
		clone.URLs[k] = make(map[string]string)
		for subK, subV := range v {
			clone.URLs[k][subK] = subV
		}
	}

	return clone
}
