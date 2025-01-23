package store

type StreamInfo struct {
	Title   string                       `json:"title"`
	TvgID   string                       `json:"tvg_id"`
	TvgChNo string                       `json:"tvg_ch"`
	TvgType string						 `json:"tvg_type"`
	LogoURL string                       `json:"logo"`
	Group   string                       `json:"group"`
	URLs    map[string]map[string]string `json:"-"`
}
