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
	"strings"
	"sync"

	"m3u-stream-merger/config"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/utils"
)

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

	tmpPath := config.GetEPGTmpPath()
	if err := mergeXMLTV(sources, tmpPath); err != nil {
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

	if _, err := io.Copy(f, body); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return p.fallback(finalPath, fmt.Errorf("write: %w", err))
	}
	f.Close()

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
// Programmes from all sources are included as-is.
func mergeXMLTV(sources []string, outputPath string) error {
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
		if err := streamXMLTVElements(src, "channel", seenChannels, out); err != nil {
			// Non-fatal: log at caller level, continue with other sources.
			_ = err
		}
	}

	// Second pass: all <programme> elements.
	for _, src := range sources {
		if err := streamXMLTVElements(src, "programme", nil, out); err != nil {
			_ = err
		}
	}

	_, err = out.WriteString("</tv>\n")
	return err
}

// streamXMLTVElements reads srcPath and copies every top-level element with the
// given local name to out.  When seen is non-nil it is used to deduplicate by
// the element's "id" attribute (first occurrence wins).
func streamXMLTVElements(srcPath, elementName string, seen map[string]bool, out io.Writer) error {
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

		// Deduplicate channels by id attribute.
		if seen != nil {
			id := attrValue(start, "id")
			if id != "" {
				if seen[id] {
					if err := dec.Skip(); err != nil {
						return err
					}
					continue
				}
				seen[id] = true
			}
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
