package sourceproc

import (
	"fmt"
	"slices"
)

// StreamURL: flat slice costs 87 B/stream vs 3416 B for a per-stream xsync map.
type StreamURL struct {
	M3UIndex string `json:"i"`
	Hash     string `json:"h"`
	LineNum  int    `json:"n"`
	URL      string `json:"u"`
}

type StreamInfo struct {
	Title       string      `json:"title"`
	TvgID       string      `json:"tvg_id"`
	TvgChNo     string      `json:"tvg_ch"`
	TvgType     string      `json:"tvg_type"`
	LogoURL     string      `json:"logo"`
	Group       string      `json:"group"`
	URLs        []StreamURL `json:"urls,omitempty"`
	SourceM3U   string      `json:"source_m3u"`
	SourceIndex int         `json:"source_index"`
}

func (s *StreamInfo) AddURL(m3uIndex, hash string, lineNum int, url string) {
	for i := range s.URLs {
		if s.URLs[i].M3UIndex == m3uIndex && s.URLs[i].Hash == hash {
			return
		}
	}
	s.URLs = append(s.URLs, StreamURL{M3UIndex: m3uIndex, Hash: hash, LineNum: lineNum, URL: url})
}

// URLsForIndex returns the hash -> "lineNum:::url" shape the load balancer parses.
func (s *StreamInfo) URLsForIndex(m3uIndex string) map[string]string {
	var urls map[string]string
	for _, u := range s.URLs {
		if u.M3UIndex != m3uIndex {
			continue
		}
		if urls == nil {
			urls = make(map[string]string, 4)
		}
		urls[u.Hash] = fmt.Sprintf("%d:::%s", u.LineNum, u.URL)
	}

	return urls
}

// Clip so a later merge append cannot write into the parsed stream's array.
func (s *StreamInfo) cloneForStore() *StreamInfo {
	clone := *s
	clone.URLs = slices.Clip(s.URLs)

	return &clone
}
