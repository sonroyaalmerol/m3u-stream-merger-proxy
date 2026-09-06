package sourceproc

import (
	"crypto/sha3"
	"encoding/hex"
	"regexp"
	"strings"

	"m3u-stream-merger/logger"
	"m3u-stream-merger/utils"
)

var (
	// attributeRegex matches M3U attributes in the format key="value"
	attributeRegex = regexp.MustCompile(`([a-zA-Z0-9_-]+)="([^"]*)"`)
)

// parseLine parses a single M3U line into a StreamInfo
func parseLine(line string, nextLine *LineDetails, m3uIndex string) *StreamInfo {
	logger.Default.Debugf("Parsing line: %s", line)
	logger.Default.Debugf("Next line: %s", nextLine.Content)

	cleanUrl := strings.TrimSpace(nextLine.Content)
	stream := &StreamInfo{}

	matches := attributeRegex.FindAllStringSubmatch(line, -1)
	lineWithoutPairs := line

	for _, match := range matches {
		key := strings.TrimSpace(match[1])
		value := strings.TrimSpace(match[2])

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
		lineWithoutPairs = strings.Replace(lineWithoutPairs, match[0], "", 1)
	}

	if commaSplit := strings.SplitN(lineWithoutPairs, ",", 2); len(commaSplit) > 1 {
		stream.Title = utils.TvgNameParser(strings.TrimSpace(commaSplit[1]))
	}

	if stream.Title == "" {
		return nil
	}

	h := sha3.Sum224([]byte(cleanUrl))
	urlHash := hex.EncodeToString(h[:])

	stream.SourceM3U = m3uIndex
	stream.SourceIndex = nextLine.LineNum
	stream.AddURL(m3uIndex, urlHash, nextLine.LineNum, cleanUrl)

	return stream
}

// formatStreamEntry formats a stream entry for M3U output
func formatStreamEntry(baseURL string, stream *StreamInfo) string {
	var entry strings.Builder

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
	entry.WriteString(GenerateStreamURL(baseURL, stream))
	entry.WriteString("\n")

	return entry.String()
}
