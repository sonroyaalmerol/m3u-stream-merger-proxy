package store

type StreamInfo struct {
	Title   string      `json:"title"`
	TvgID   string      `json:"-"`
	TvgChNo string      `json:"-"`
	LogoURL string      `json:"-"`
	Group   string      `json:"-"`
	URLs    map[int]int `json:"urls"`
}
