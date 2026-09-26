package sourceproc

import (
	"encoding/base64"
	"strconv"
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

const slugBufSize = 48

type streamParser struct {
	stream StreamInfo
	url    [1]StreamURL
}

func parseLine(line string, nextLine *LineDetails, m3uIndex string) *StreamInfo {
	var parser streamParser
	return parser.parseLine(line, nextLine, m3uIndex)
}

// parseLine parses a single M3U line into a StreamInfo.
func (p *streamParser) parseLine(line string, nextLine *LineDetails, m3uIndex string) *StreamInfo {
	cleanUrl := strings.TrimSpace(nextLine.Content)
	p.stream = StreamInfo{URLs: p.url[:0]}
	stream := &p.stream
	catchupType := ""
	catchupSource := ""
	catchupDays := 0

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
		case "catchup":
			catchupType = strings.ToLower(value)
		case "catchup-days":
			catchupDays, _ = strconv.Atoi(value)
		case "catchup-source", "catchup-url":
			catchupSource = value
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
	if catchupType == "" && catchupSource != "" {
		catchupType = "default"
	}
	if catchupDays < 1 || (catchupType != "xtream" && catchupType != "xc" && catchupSource == "") {
		catchupDays = 0
		catchupType = ""
		catchupSource = ""
	}
	stream.AddStreamURL(StreamURL{
		M3UIndex:      m3uIndex,
		LineNum:       nextLine.LineNum,
		URL:           cleanUrl,
		CatchupDays:   catchupDays,
		CatchupType:   catchupType,
		CatchupSource: catchupSource,
	})

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

// entryWriter is satisfied by *bufio.Writer and *strings.Builder.
type entryWriter interface {
	WriteString(string) (int, error)
	Write([]byte) (int, error)
}

// entrySink defers error handling to the end of the entry instead of checking every field write.
type entrySink struct {
	w   entryWriter
	err error
}

func (e *entrySink) str(s string) {
	if e.err == nil {
		_, e.err = e.w.WriteString(s)
	}
}

func (e *entrySink) bytes(value []byte) {
	if e.err == nil {
		_, e.err = e.w.Write(value)
	}
}

func (e *entrySink) tag(key, value string) {
	if value == "" {
		return
	}
	e.str(" ")
	e.str(key)
	e.str(`="`)
	e.str(value)
	e.str(`"`)
}

// writeStreamEntry writes one M3U entry straight to w, so no per-entry string is built.
// slugBuf is caller-owned scratch; a local array would escape through the writer interface.
func writeStreamEntry(w entryWriter, baseURL string, sum [28]byte, stream *StreamInfo, slugBuf []byte) error {
	e := entrySink{w: w}
	slug := base64.RawURLEncoding.AppendEncode(slugBuf[:0], sum[:])

	e.str("#EXTINF:-1")
	e.tag("tvg-id", stream.TvgID)
	e.tag("tvg-chno", stream.TvgChNo)
	e.tag("tvg-logo", stream.LogoURL)
	e.tag("tvg-group", stream.Group)
	e.tag("group-title", stream.Group)
	e.tag("tvg-type", stream.TvgType)
	e.tag("tvg-name", stream.Title)
	if stream.CatchupDays > 0 {
		e.tag("catchup", "default")
		e.tag("catchup-days", strconv.Itoa(stream.CatchupDays))
		e.str(` catchup-source="`)
		e.str(baseURL)
		e.str("/p/catchup/")
		e.bytes(slug)
		e.str(`?start=${start}&duration=${duration}"`)
	}

	e.str(",")
	e.str(stream.Title)
	e.str("\n")
	e.str(baseURL)
	e.str("/p/stream/")
	e.bytes(slug)
	e.str("\n")

	return e.err
}

func formatStreamEntry(baseURL string, sum [28]byte, stream *StreamInfo) string {
	var entry strings.Builder
	_ = writeStreamEntry(&entry, baseURL, sum, stream, make([]byte, 0, slugBufSize))

	return entry.String()
}
