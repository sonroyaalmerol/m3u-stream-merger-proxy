package database

type StreamInfo struct {
	Slug    string
	Title   string
	TvgID   string
	TvgChNo string
	LogoURL string
	Group   string
	URLs    []StreamURL
}

type StreamURL struct {
	Content  string
	M3UIndex int
}
