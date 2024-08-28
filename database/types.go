package database

type StreamInfo struct {
	Slug    string         `json:"slug"`
	Title   string         `json:"title"`
	TvgID   string         `json:"tvg_id"`
	TvgChNo string         `json:"tvg_chno"`
	LogoURL string         `json:"logo_url"`
	Group   string         `json:"group_name"`
	URLs    map[int]string `json:"urls"`
}
