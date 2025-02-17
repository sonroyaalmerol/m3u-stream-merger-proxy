package proxy

const (
	StatusClientClosed   = 0 // client has been closed
	StatusServerError    = 1 // server-side error (non-EOF)
	StatusEOF            = 2 // server-side EOF reached
	StatusM3U8Parsed     = 3 // Successfully parsed M3U8 stream
	StatusM3U8ParseError = 4 // Failed to parse as M3U8 stream
	StatusIncompatible   = 5
)
