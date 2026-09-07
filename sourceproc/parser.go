package sourceproc

import (
	"encoding/base64"
	"strings"

	"m3u-stream-merger/utils"
)

func isKeyChar(c byte) bool {
	return c == '-' || c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// forEachAttr walks k="v" pairs left to right, non-overlapping, matching attributeRegex.
func forEachAttr(s string, fn func(key, val string)) {
	i := 0
	for i < len(s) {
		off := strings.Index(s[i:], `="`)
		if off < 0 {
			return
		}
		pos := i + off
		ks := pos
		for ks > i && isKeyChar(s[ks-1]) {
			ks--
		}
		if ks == pos {
			i = pos + 2
			continue
		}
		end := strings.IndexByte(s[pos+2:], '"')
		if end < 0 {
			return
		}
		fn(s[ks:pos], s[pos+2:pos+2+end])
		i = pos + 2 + end + 1
	}
}

const slabSize = 256

// streamSlab batches per-stream allocations so a worker pays one make per slabSize lines.
type streamSlab struct {
	streams []StreamInfo
	urls    []StreamURL
}

func (a *streamSlab) newStream() *StreamInfo {
	if len(a.streams) == 0 {
		a.streams = make([]StreamInfo, slabSize)
	}
	s := &a.streams[0]
	a.streams = a.streams[1:]

	// Capped at one so a later merge append reallocates instead of writing into the next slab entry.
	if len(a.urls) == 0 {
		a.urls = make([]StreamURL, slabSize)
	}
	s.URLs = a.urls[0:0:1]
	a.urls = a.urls[1:]

	return s
}

func parseLine(line string, nextLine *LineDetails, m3uIndex string) *StreamInfo {
	var slab streamSlab
	return slab.parseLine(line, nextLine, m3uIndex)
}

// parseLine parses a single M3U line into a StreamInfo
func (a *streamSlab) parseLine(line string, nextLine *LineDetails, m3uIndex string) *StreamInfo {
	cleanUrl := strings.TrimSpace(nextLine.Content)
	stream := a.newStream()

	forEachAttr(line, func(key, value string) {
		value = strings.TrimSpace(value)

		switch strings.ToLower(key) {
		case "tvg-id":
			stream.TvgID = utils.TvgIdParser(value)
		case "tvg-chno", "channel-id", "channel-number":
			stream.TvgChNo = utils.TvgChNoParser(value)
		case "tvg-name":
			stream.Title = utils.TvgNameParser(value)
		case "tvg-type":
			stream.TvgType = utils.TvgTypeParser(value)
		case "tvg-group", "group-title":
			stream.Group = utils.GroupTitleParser(value)
		case "tvg-logo":
			stream.LogoURL = utils.TvgLogoParser(value)
		}
	})

	if commaIdx := indexUnquotedComma(line); commaIdx >= 0 {
		stream.Title = utils.TvgNameParser(strings.TrimSpace(line[commaIdx+1:]))
	}

	if stream.Title == "" {
		return nil
	}

	stream.SourceM3U = m3uIndex
	stream.SourceIndex = nextLine.LineNum
	stream.AddURL(m3uIndex, nextLine.LineNum, cleanUrl)

	return stream
}

// indexUnquotedComma finds the attribute-section terminator, the first comma outside quoted values.
func indexUnquotedComma(s string) int {
	inQuote := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQuote = !inQuote
		case ',':
			if !inQuote {
				return i
			}
		}
	}
	return -1
}

// formatStreamEntry formats a stream entry for M3U output
func formatStreamEntry(baseURL string, sum [28]byte, stream *StreamInfo) string {
	var entry strings.Builder
	entry.Grow(140 + len(baseURL) + 2*len(stream.Title) + len(stream.LogoURL) +
		2*len(stream.Group) + len(stream.TvgID) + len(stream.TvgType) + len(stream.TvgChNo))

	writeTag := func(key, value string) {
		if value == "" {
			return
		}
		entry.WriteString(" ")
		entry.WriteString(key)
		entry.WriteString(`="`)
		entry.WriteString(value)
		entry.WriteString(`"`)
	}

	entry.WriteString("#EXTINF:-1")
	writeTag("tvg-id", stream.TvgID)
	writeTag("tvg-chno", stream.TvgChNo)
	writeTag("tvg-logo", stream.LogoURL)
	writeTag("tvg-group", stream.Group)
	writeTag("group-title", stream.Group)
	writeTag("tvg-type", stream.TvgType)
	writeTag("tvg-name", stream.Title)

	entry.WriteString(",")
	entry.WriteString(stream.Title)
	entry.WriteString("\n")
	entry.WriteString(baseURL)
	entry.WriteString("/p/stream/")
	var slug [48]byte
	n := base64.RawURLEncoding.EncodedLen(len(sum))
	base64.RawURLEncoding.Encode(slug[:n], sum[:])
	entry.Write(slug[:n])
	entry.WriteString("\n")

	return entry.String()
}
