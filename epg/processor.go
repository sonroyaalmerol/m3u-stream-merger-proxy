package epg

import (
	"compress/gzip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"m3u-stream-merger/config"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/utils"
)

// defaultMaxEPGMB is the upper bound on a decompressed EPG file when
// EPG_MAX_SIZE_MB is not set.  500 MB comfortably covers large full-day guides
// while preventing a decompression bomb from filling the disk.
const defaultMaxEPGMB = 500

// maxEPGBytes returns the decompression size limit in bytes, reading
// EPG_MAX_SIZE_MB from the environment and falling back to defaultMaxEPGMB.
var maxEPGBytes = func() int64 {
	if v := os.Getenv("EPG_MAX_SIZE_MB"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n * 1024 * 1024
		}
	}
	return defaultMaxEPGMB * 1024 * 1024
}()

// Processor downloads EPG sources and merges them into a single XMLTV file.
type Processor struct {
	logger logger.Logger
}

func NewProcessor(log logger.Logger) *Processor {
	return &Processor{logger: log}
}

// Run downloads all configured EPG_URL_X sources and merges them into
// a single XMLTV file served at /epg.xml.  It is a no-op when no
// EPG_URL_X variables are set.
func (p *Processor) Run(ctx context.Context) error {
	indexes := utils.GetEPGIndexes()
	if len(indexes) == 0 {
		return nil
	}

	if err := os.MkdirAll(config.GetEPGDirPath(), 0755); err != nil {
		return fmt.Errorf("epg: mkdir: %w", err)
	}

	type dlResult struct {
		index string
		path  string
		err   error
	}
	results := make(chan dlResult, len(indexes))

	var wg sync.WaitGroup
	for _, idx := range indexes {
		wg.Add(1)
		go func(idx string) {
			defer wg.Done()
			path, err := p.downloadSource(ctx, idx)
			results <- dlResult{index: idx, path: path, err: err}
		}(idx)
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	var sources []string
	for res := range results {
		if res.err != nil {
			p.logger.Warnf("epg: skipping index %s: %v", res.index, res.err)
			continue
		}
		sources = append(sources, res.path)
	}

	if len(sources) == 0 {
		return fmt.Errorf("epg: all sources failed to download")
	}

	tvgIDs := loadTvgIDs()
	channelMap := utils.GetEPGChannelMappings()

	tmpPath := config.GetEPGTmpPath()
	if err := mergeXMLTV(sources, tmpPath, tvgIDs, channelMap); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("epg: merge: %w", err)
	}

	return os.Rename(tmpPath, config.GetEPGPath())
}

// downloadSource fetches one EPG source, caches it, and returns the local path.
func (p *Processor) downloadSource(ctx context.Context, idx string) (string, error) {
	epgURL := os.Getenv(fmt.Sprintf("EPG_URL_%s", idx))
	if epgURL == "" {
		return "", fmt.Errorf("EPG_URL_%s is not set", idx)
	}

	// Local file passthrough.
	if after, ok := strings.CutPrefix(epgURL, "file://"); ok {
		return after, nil
	}

	finalPath := config.GetEPGSourcePath(idx)
	tmpPath := config.GetEPGSourceTmpPath(idx)

	req, err := http.NewRequestWithContext(ctx, "GET", epgURL, nil)
	if err != nil {
		return p.fallback(finalPath, fmt.Errorf("create request: %w", err))
	}
	req.Header.Set("User-Agent", utils.GetEnv("USER_AGENT"))

	resp, err := utils.HTTPClient.Do(req)
	if err != nil {
		return p.fallback(finalPath, fmt.Errorf("HTTP: %w", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return p.fallback(finalPath, fmt.Errorf("HTTP status %d", resp.StatusCode))
	}

	f, err := os.Create(tmpPath)
	if err != nil {
		return p.fallback(finalPath, fmt.Errorf("create tmp: %w", err))
	}

	body, err := decompressIfNeeded(resp, epgURL)
	if err != nil {
		f.Close()
		os.Remove(tmpPath)
		return p.fallback(finalPath, fmt.Errorf("decompress: %w", err))
	}
	defer body.Close()

	// Guard against decompression bombs: cap the amount written to disk.
	// LimitReader returns EOF after maxEPGBytes, so we detect the breach by
	// comparing the byte count against the limit after the copy.
	limited := io.LimitReader(body, maxEPGBytes+1)
	n, err := io.Copy(f, limited)
	f.Close()
	if err != nil {
		os.Remove(tmpPath)
		return p.fallback(finalPath, fmt.Errorf("write: %w", err))
	}
	if n > maxEPGBytes {
		os.Remove(tmpPath)
		return p.fallback(finalPath, fmt.Errorf("EPG source exceeds maximum allowed size (%d MB)", maxEPGBytes/1024/1024))
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		// Couldn't rename — use the tmp file directly.
		return tmpPath, nil
	}
	return finalPath, nil
}

// fallback returns the cached file if it exists, otherwise propagates the error.
func (p *Processor) fallback(cachedPath string, origErr error) (string, error) {
	if _, err := os.Stat(cachedPath); err == nil {
		p.logger.Warnf("epg: download failed (%v), using cached file %s", origErr, cachedPath)
		return cachedPath, nil
	}
	return "", origErr
}

// loadTvgIDs reads the tvg-id set persisted by the M3U processor.  When the
// file doesn't exist (e.g. EPG ran before any M3U sync) an empty map is
// returned, which disables filtering so all channels are kept.
func loadTvgIDs() map[string]struct{} {
	data, err := os.ReadFile(config.GetEPGTvgIDsPath())
	if err != nil {
		return nil // no filter
	}
	ids := make(map[string]struct{})
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		if line != "" {
			ids[line] = struct{}{}
		}
	}
	return ids
}

// decompressIfNeeded wraps resp.Body in a gzip reader when the response
// appears to be gzip-compressed but is NOT already transparently decompressed
// by Go's HTTP transport (i.e. the body is a raw .gz file rather than one
// signalled with Content-Encoding: gzip).
//
// Detection order:
//  1. URL path ends with ".gz"
//  2. Content-Type is application/gzip or application/x-gzip
//
// Note: Go's http.Transport handles Content-Encoding: gzip transparently, so
// we only need to take action for the "file body is gzip" case.
func decompressIfNeeded(resp *http.Response, rawURL string) (io.ReadCloser, error) {
	ct := resp.Header.Get("Content-Type")
	urlPath := strings.ToLower(strings.SplitN(rawURL, "?", 2)[0])

	needsGzip := strings.HasSuffix(urlPath, ".gz") ||
		strings.Contains(ct, "application/gzip") ||
		strings.Contains(ct, "application/x-gzip")

	if !needsGzip {
		return resp.Body, nil
	}

	gr, err := gzip.NewReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("gzip reader: %w", err)
	}
	// Return a ReadCloser that closes both the gzip reader and the underlying body.
	return struct {
		io.Reader
		io.Closer
	}{gr, multiCloser{gr, resp.Body}}, nil
}

type multiCloser [2]io.Closer

func (mc multiCloser) Close() error {
	err1 := mc[0].Close()
	err2 := mc[1].Close()
	if err1 != nil {
		return err1
	}
	return err2
}

// mergeXMLTV merges multiple XMLTV source files into a single output file.
// Channels are deduplicated by their id attribute (first occurrence wins).
// When tvgIDs is non-nil only channels/programmes whose id/channel attribute
// appears in that set are written; a nil map means "keep everything".
// channelMap remaps EPG channel ids to M3U tvg-ids (epgID → tvgID); may be nil.
func mergeXMLTV(sources []string, outputPath string, tvgIDs map[string]struct{}, channelMap map[string]string) error {
	out, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := out.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n"); err != nil {
		return err
	}
	if _, err := out.WriteString(`<tv generator-info-name="m3u-stream-merger-proxy">` + "\n"); err != nil {
		return err
	}

	seenChannels := make(map[string]bool)

	// First pass: unique <channel> elements.
	for _, src := range sources {
		if err := streamXMLTVElements(src, "channel", seenChannels, tvgIDs, channelMap, out); err != nil {
			_ = err
		}
	}

	// Second pass: all <programme> elements.
	for _, src := range sources {
		if err := streamXMLTVElements(src, "programme", nil, tvgIDs, channelMap, out); err != nil {
			_ = err
		}
	}

	_, err = out.WriteString("</tv>\n")
	return err
}

// streamXMLTVElements reads srcPath and copies every top-level element with the
// given local name to out.
//   - seen: when non-nil, deduplicate by the element's "id" attribute
//   - tvgIDs: when non-nil, skip elements whose identity attribute (id for
//     channels, channel for programmes) is not in the set
//   - channelMap: when non-nil, remaps EPG channel ids to M3U tvg-ids before
//     filtering and deduplication; the identity attribute is rewritten in output
func streamXMLTVElements(srcPath, elementName string, seen map[string]bool, tvgIDs map[string]struct{}, channelMap map[string]string, out io.Writer) error {
	f, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer f.Close()

	dec := xml.NewDecoder(f)
	dec.Strict = false
	dec.CharsetReader = func(charset string, r io.Reader) (io.Reader, error) {
		// Accept any charset; Go's xml decoder handles UTF-8 natively.
		return r, nil
	}

	enc := xml.NewEncoder(out)

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Best-effort: return error so caller can log it.
			return fmt.Errorf("decode %s: %w", filepath.Base(srcPath), err)
		}

		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != elementName {
			continue
		}

		// For channels the identity attr is "id"; for programmes it is "channel".
		identityAttr := "id"
		if elementName == "programme" {
			identityAttr = "channel"
		}
		identity := attrValue(start, identityAttr)

		// Channel remapping: if the EPG id is mapped to a different M3U tvg-id,
		// rewrite the identity attribute on the start element before filtering.
		if channelMap != nil && identity != "" {
			if mapped, ok := channelMap[identity]; ok {
				identity = mapped
				setAttr(&start, identityAttr, mapped)
			}
		}

		// Filter: skip elements whose identity is not in the tvg-id set.
		if tvgIDs != nil && identity != "" {
			if _, ok := tvgIDs[identity]; !ok {
				if err := dec.Skip(); err != nil {
					return err
				}
				continue
			}
		}

		// Deduplicate channels by id attribute.
		if seen != nil && identity != "" {
			if seen[identity] {
				if err := dec.Skip(); err != nil {
					return err
				}
				continue
			}
			seen[identity] = true
		}

		if err := copyElement(dec, enc, start); err != nil {
			return err
		}
		out.Write([]byte("\n")) //nolint:errcheck
	}

	return enc.Flush()
}

// copyElement encodes start and all nested tokens up to the matching end element.
func copyElement(dec *xml.Decoder, enc *xml.Encoder, start xml.StartElement) error {
	if err := enc.EncodeToken(start); err != nil {
		return err
	}
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		}
		if err := enc.EncodeToken(tok); err != nil {
			return err
		}
	}
	return nil
}

func attrValue(el xml.StartElement, name string) string {
	for _, a := range el.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// setAttr updates the named attribute on el in place, or appends it if absent.
func setAttr(el *xml.StartElement, name, value string) {
	for i, a := range el.Attr {
		if a.Name.Local == name {
			el.Attr[i].Value = value
			return
		}
	}
	el.Attr = append(el.Attr, xml.Attr{Name: xml.Name{Local: name}, Value: value})
}
