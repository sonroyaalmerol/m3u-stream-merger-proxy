package store

type StreamInfo struct {
	Title   string         `json:"title"`
	TvgID   string         `json:"tvg_id"`
	TvgChNo string         `json:"tvg_ch"`
	LogoURL string         `json:"logo"`
	Group   string         `json:"group"`
	URLs    map[int]string `json:"-"`
}
