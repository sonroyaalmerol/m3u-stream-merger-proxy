package m3u

type StreamInfo struct {
	Title   string
	TvgID   string
	LogoURL string
	Group   string
	URLs    []StreamURL
}

type StreamURL struct {
	Content  string
	M3UIndex int
}
