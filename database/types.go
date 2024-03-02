package database

type StreamInfo struct {
	DbId    int64
	Title   string
	TvgID   string
	LogoURL string
	Group   string
	URLs    []StreamURL
}

type StreamURL struct {
	DbId           int64
	Content        string
	M3UIndex       int
	MaxConcurrency int
}
