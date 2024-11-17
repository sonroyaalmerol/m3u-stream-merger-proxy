package store

type StreamInfo struct {
	Slug    string         `json:"-"`
	Title   string         `json:"title"`
	TvgID   string         `json:"-"`
	TvgChNo string         `json:"-"`
	LogoURL string         `json:"-"`
	Group   string         `json:"-"`
	URLs    map[int]string `json:"urls"`
}
