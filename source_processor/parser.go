package sourceproc

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"m3u-stream-merger/config"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/utils"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/sha3"
)

// parseLine parses a single M3U line into a StreamInfo
func parseLine(line string, nextLine *LineDetails, m3uIndex string) *StreamInfo {
	logger.Default.Debugf("Parsing line: %s", line)
	logger.Default.Debugf("Next line: %s", nextLine.Content)

	cleanUrl := strings.TrimSpace(nextLine.Content)
	stream := &StreamInfo{
		URLs: make(map[string]map[string]string),
	}

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

	// Initialize URL map for this index
	if stream.URLs[m3uIndex] == nil {
		stream.URLs[m3uIndex] = make(map[string]string)
	}

	encodedUrl := base64.StdEncoding.EncodeToString([]byte(cleanUrl))

	if stream.Title == "" {
		logger.Default.Debugf("Stream missing title, skipping: %s", line)
		return nil
	}

	err := os.MkdirAll(config.GetStreamsDirPath(), os.ModePerm)
	if err != nil {
		logger.Default.Debugf("Error creating stream cache folder: %s -> %v", config.GetStreamsDirPath(), err)
	}

	base64Title := base64.StdEncoding.EncodeToString([]byte(stream.Title))
	h := sha3.Sum224([]byte(cleanUrl))
	urlHash := hex.EncodeToString(h[:])

	fileName := fmt.Sprintf("%s_%s|%s", base64Title, m3uIndex, urlHash)
	filePath := filepath.Join(config.GetStreamsDirPath(), fileName)

	stream.SourceM3U = m3uIndex
	stream.SourceIndex = nextLine.LineNum

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		content := fmt.Sprintf("%d:::%s", nextLine.LineNum, encodedUrl)
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			logger.Default.Debugf("Error indexing stream: %s (#%s) -> %v", stream.Title, m3uIndex, err)
		}
		// Initialize maps if not already initialized
		if stream.URLs == nil {
			stream.URLs = make(map[string]map[string]string)
		}
		if stream.URLs[m3uIndex] == nil {
			stream.URLs[m3uIndex] = make(map[string]string)
		}

		// Add the URL to the map
		stream.URLs[m3uIndex][urlHash] = fmt.Sprintf("%d:::%s", nextLine.LineNum, cleanUrl)
	}

	return stream
}

// mergeURLs merges URLs from source into target StreamInfo
func mergeURLs(target, source *StreamInfo) {
	target.Lock()
	defer target.Unlock()
	source.RLock()
	defer source.RUnlock()

	for idx, innerMap := range source.URLs {
		if target.URLs[idx] == nil {
			target.URLs[idx] = make(map[string]string)
		}
		for hashKey, url := range innerMap {
			if _, exists := target.URLs[idx][hashKey]; !exists {
				target.URLs[idx][hashKey] = url
			}
		}
	}
}

// mergeAttributes merges attributes from source into target StreamInfo.
// If the target does not already have an attribute set, the value from the
// source will be assigned.
func mergeAttributes(target, source *StreamInfo) {
	target.Lock()
	defer target.Unlock()
	source.RLock()
	defer source.RUnlock()

	if target.TvgID == "" {
		target.TvgID = source.TvgID
	}
	if target.TvgChNo == "" {
		target.TvgChNo = source.TvgChNo
	}
	if target.Title == "" {
		target.Title = source.Title
	}
	if target.TvgType == "" {
		target.TvgType = source.TvgType
	}
	if target.Group == "" {
		target.Group = source.Group
	}
	if target.LogoURL == "" {
		target.LogoURL = source.LogoURL
	}
}

// formatStreamEntry formats a stream entry for M3U output
func formatStreamEntry(baseURL string, stream *StreamInfo) string {
	var entry strings.Builder

	extInfTags := []string{"#EXTINF:-1"}

	if stream.TvgID != "" {
		extInfTags = append(extInfTags, fmt.Sprintf("tvg-id=\"%s\"", stream.TvgID))
	}
	if stream.TvgChNo != "" {
		extInfTags = append(extInfTags, fmt.Sprintf("tvg-chno=\"%s\"", stream.TvgChNo))
	}
	if stream.LogoURL != "" {
		extInfTags = append(extInfTags, fmt.Sprintf("tvg-logo=\"%s\"", stream.LogoURL))
	}
	if stream.Group != "" {
		extInfTags = append(extInfTags, fmt.Sprintf("tvg-group=\"%s\"", stream.Group))
		extInfTags = append(extInfTags, fmt.Sprintf("group-title=\"%s\"", stream.Group))
	}
	if stream.TvgType != "" {
		extInfTags = append(extInfTags, fmt.Sprintf("tvg-type=\"%s\"", stream.TvgType))
	}
	if stream.Title != "" {
		extInfTags = append(extInfTags, fmt.Sprintf("tvg-name=\"%s\"", stream.Title))
	}

	entry.WriteString(fmt.Sprintf("%s,%s\n", strings.Join(extInfTags, " "), stream.Title))
	entry.WriteString(GenerateStreamURL(baseURL, stream))
	entry.WriteString("\n")

	return entry.String()
}
