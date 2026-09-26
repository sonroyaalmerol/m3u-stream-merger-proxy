package sourceproc

import (
	"fmt"
	"strconv"

	"github.com/cespare/xxhash"
)

// StreamURL: flat slice costs 87 B/stream vs 3416 B for a per-stream xsync map.
type StreamURL struct {
	M3UIndex    string `json:"i"`
	LineNum     int    `json:"n"`
	URL         string `json:"u"`
	CatchupDays int    `json:"c,omitempty"`
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
	CatchupDays int         `json:"catchup_days,omitempty"`
	backing     []byte
}

func (s *StreamInfo) AddURL(m3uIndex string, lineNum int, url string) {
	s.AddStreamURL(StreamURL{M3UIndex: m3uIndex, LineNum: lineNum, URL: url})
}

func (s *StreamInfo) AddStreamURL(streamURL StreamURL) {
	for i := range s.URLs {
		if s.URLs[i].M3UIndex == streamURL.M3UIndex && s.URLs[i].URL == streamURL.URL {
			if streamURL.CatchupDays > s.URLs[i].CatchupDays {
				s.URLs[i].CatchupDays = streamURL.CatchupDays
			}
			if streamURL.CatchupDays > s.CatchupDays {
				s.CatchupDays = streamURL.CatchupDays
			}
			return
		}
	}
	s.URLs = append(s.URLs, streamURL)
	if streamURL.CatchupDays > s.CatchupDays {
		s.CatchupDays = streamURL.CatchupDays
	}
}

// URLKey is a stream URL's load balancer sub-index; hashed so credentials never reach ids or logs.
func URLKey(url string) string {
	return strconv.FormatUint(xxhash.Sum64String(url), 36)
}

// URLsForIndex returns the key -> "lineNum:::url" shape the load balancer parses.
func (s *StreamInfo) URLsForIndex(m3uIndex string) map[string]string {
	var urls map[string]string
	for _, u := range s.URLs {
		if u.M3UIndex != m3uIndex {
			continue
		}
		if urls == nil {
			urls = make(map[string]string, 4)
		}
		urls[URLKey(u.URL)] = fmt.Sprintf("%d:::%s", u.LineNum, u.URL)
	}

	return urls
}
