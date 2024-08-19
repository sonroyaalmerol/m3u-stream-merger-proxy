package database

type StreamInfo struct {
	Slug    string
	Title   string
	TvgID   string
	TvgChNo string
	LogoURL string
	Group   string
	URLs    map[int]string
}
