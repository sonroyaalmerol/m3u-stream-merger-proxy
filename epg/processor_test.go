package epg

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m3u-stream-merger/config"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/utils"
)

// xmltvDoc is a minimal helper for parsing the merged output in tests.
type xmltvDoc struct {
	XMLName    xml.Name       `xml:"tv"`
	Channels   []xmlChannel   `xml:"channel"`
	Programmes []xmlProgramme `xml:"programme"`
}

type xmlChannel struct {
	ID          string `xml:"id,attr"`
	DisplayName string `xml:"display-name"`
}

type xmlProgramme struct {
	Channel string `xml:"channel,attr"`
	Title   string `xml:"title"`
}

func parseMergedXML(t *testing.T, path string) xmltvDoc {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open merged xml: %v", err)
	}
	defer f.Close()

	var doc xmltvDoc
	if err := xml.NewDecoder(f).Decode(&doc); err != nil {
		t.Fatalf("decode merged xml: %v", err)
	}
	return doc
}

// setupTestConfig redirects data/temp paths to a temp dir so tests never touch
// /m3u-proxy/data.  It also resets the EPG index cache on cleanup.
func setupTestConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	config.SetConfig(&config.Config{
		DataPath: filepath.Join(dir, "data") + "/",
		TempPath: filepath.Join(dir, "tmp") + "/",
	})
	t.Cleanup(utils.ResetCaches)
}

func xmltvSource(channels []xmlChannel, programmes []xmlProgramme) string {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	sb.WriteString("<tv>\n")
	for _, ch := range channels {
		sb.WriteString(fmt.Sprintf(`<channel id=%q><display-name>%s</display-name></channel>`+"\n",
			ch.ID, ch.DisplayName))
	}
	for _, pr := range programmes {
		sb.WriteString(fmt.Sprintf(`<programme channel=%q><title>%s</title></programme>`+"\n",
			pr.Channel, pr.Title))
	}
	sb.WriteString("</tv>\n")
	return sb.String()
}

// gzipBytes compresses data with gzip and returns the compressed bytes.
func gzipBytes(t *testing.T, data string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write([]byte(data)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// ---------------------------------------------------------------------------
// mergeXMLTV unit tests
// ---------------------------------------------------------------------------

// TestMergeXMLTV_Basic verifies that channels and programmes from two sources
// end up in the merged output.
func TestMergeXMLTV_Basic(t *testing.T) {
	dir := t.TempDir()

	src1 := filepath.Join(dir, "src1.xml")
	src2 := filepath.Join(dir, "src2.xml")
	out := filepath.Join(dir, "out.xml")

	os.WriteFile(src1, []byte(xmltvSource(
		[]xmlChannel{{ID: "ch1", DisplayName: "Channel 1"}},
		[]xmlProgramme{{Channel: "ch1", Title: "Show A"}},
	)), 0644)
	os.WriteFile(src2, []byte(xmltvSource(
		[]xmlChannel{{ID: "ch2", DisplayName: "Channel 2"}},
		[]xmlProgramme{{Channel: "ch2", Title: "Show B"}},
	)), 0644)

	if err := mergeXMLTV([]string{src1, src2}, out, nil, nil); err != nil {
		t.Fatalf("mergeXMLTV: %v", err)
	}

	doc := parseMergedXML(t, out)
	if len(doc.Channels) != 2 {
		t.Errorf("expected 2 channels, got %d", len(doc.Channels))
	}
	if len(doc.Programmes) != 2 {
		t.Errorf("expected 2 programmes, got %d", len(doc.Programmes))
	}
}

// TestMergeXMLTV_DeduplicatesChannels verifies that the same channel id from
// two sources results in only one channel entry (first wins).
func TestMergeXMLTV_DeduplicatesChannels(t *testing.T) {
	dir := t.TempDir()
	src1 := filepath.Join(dir, "src1.xml")
	src2 := filepath.Join(dir, "src2.xml")
	out := filepath.Join(dir, "out.xml")

	os.WriteFile(src1, []byte(xmltvSource(
		[]xmlChannel{{ID: "dup", DisplayName: "First"}}, nil,
	)), 0644)
	os.WriteFile(src2, []byte(xmltvSource(
		[]xmlChannel{{ID: "dup", DisplayName: "Second"}}, nil,
	)), 0644)

	if err := mergeXMLTV([]string{src1, src2}, out, nil, nil); err != nil {
		t.Fatalf("mergeXMLTV: %v", err)
	}

	doc := parseMergedXML(t, out)
	if len(doc.Channels) != 1 {
		t.Errorf("expected 1 channel after dedup, got %d", len(doc.Channels))
	}
	if doc.Channels[0].DisplayName != "First" {
		t.Errorf("expected first-occurrence channel to win, got %q", doc.Channels[0].DisplayName)
	}
}

// TestMergeXMLTV_ProgrammesFromAllSources verifies programmes are collected
// from every source even when a channel is duplicated.
func TestMergeXMLTV_ProgrammesFromAllSources(t *testing.T) {
	dir := t.TempDir()
	src1 := filepath.Join(dir, "src1.xml")
	src2 := filepath.Join(dir, "src2.xml")
	out := filepath.Join(dir, "out.xml")

	os.WriteFile(src1, []byte(xmltvSource(
		[]xmlChannel{{ID: "ch1", DisplayName: "Ch1"}},
		[]xmlProgramme{{Channel: "ch1", Title: "Morning News"}},
	)), 0644)
	os.WriteFile(src2, []byte(xmltvSource(
		[]xmlChannel{{ID: "ch1", DisplayName: "Ch1 Duplicate"}},
		[]xmlProgramme{{Channel: "ch1", Title: "Evening News"}},
	)), 0644)

	if err := mergeXMLTV([]string{src1, src2}, out, nil, nil); err != nil {
		t.Fatalf("mergeXMLTV: %v", err)
	}

	doc := parseMergedXML(t, out)
	if len(doc.Channels) != 1 {
		t.Errorf("expected 1 deduplicated channel, got %d", len(doc.Channels))
	}
	if len(doc.Programmes) != 2 {
		t.Errorf("expected 2 programmes (one per source), got %d", len(doc.Programmes))
	}
}

// TestMergeXMLTV_FilterByTvgIDs verifies that channels and programmes whose
// id/channel attribute is not in the tvg-id set are dropped from the output.
func TestMergeXMLTV_FilterByTvgIDs(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.xml")
	out := filepath.Join(dir, "out.xml")

	os.WriteFile(src, []byte(xmltvSource(
		[]xmlChannel{
			{ID: "keep", DisplayName: "Keep Me"},
			{ID: "drop", DisplayName: "Drop Me"},
		},
		[]xmlProgramme{
			{Channel: "keep", Title: "Kept Show"},
			{Channel: "drop", Title: "Dropped Show"},
		},
	)), 0644)

	tvgIDs := map[string]struct{}{"keep": {}}
	if err := mergeXMLTV([]string{src}, out, tvgIDs, nil); err != nil {
		t.Fatalf("mergeXMLTV: %v", err)
	}

	doc := parseMergedXML(t, out)
	if len(doc.Channels) != 1 || doc.Channels[0].ID != "keep" {
		t.Errorf("expected only 'keep' channel, got %+v", doc.Channels)
	}
	if len(doc.Programmes) != 1 || doc.Programmes[0].Title != "Kept Show" {
		t.Errorf("expected only 'Kept Show' programme, got %+v", doc.Programmes)
	}
}

// TestMergeXMLTV_NilFilterKeepsAll verifies that a nil tvg-id map keeps
// all channels and programmes (backwards-compatible / no-filter behaviour).
func TestMergeXMLTV_NilFilterKeepsAll(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.xml")
	out := filepath.Join(dir, "out.xml")

	os.WriteFile(src, []byte(xmltvSource(
		[]xmlChannel{{ID: "c1"}, {ID: "c2"}},
		[]xmlProgramme{{Channel: "c1"}, {Channel: "c2"}},
	)), 0644)

	if err := mergeXMLTV([]string{src}, out, nil, nil); err != nil {
		t.Fatalf("mergeXMLTV: %v", err)
	}

	doc := parseMergedXML(t, out)
	if len(doc.Channels) != 2 {
		t.Errorf("expected 2 channels with nil filter, got %d", len(doc.Channels))
	}
	if len(doc.Programmes) != 2 {
		t.Errorf("expected 2 programmes with nil filter, got %d", len(doc.Programmes))
	}
}

// ---------------------------------------------------------------------------
// Processor.Run unit tests (httptest server)
// ---------------------------------------------------------------------------

// TestProcessor_Run_NoEPGURLs verifies that Run is a no-op when no EPG_URL_X
// env vars are set.
func TestProcessor_Run_NoEPGURLs(t *testing.T) {
	setupTestConfig(t)

	p := NewProcessor(logger.Default)
	if err := p.Run(context.Background()); err != nil {
		t.Errorf("expected no error when no EPG URLs configured, got: %v", err)
	}
}

// TestProcessor_Run_DownloadsAndMerges spins up a local HTTP server serving
// two plain XMLTV files and verifies the processor produces a merged output.
func TestProcessor_Run_DownloadsAndMerges(t *testing.T) {
	setupTestConfig(t)

	src1Body := xmltvSource(
		[]xmlChannel{{ID: "s1ch1", DisplayName: "Server1 Ch1"}},
		[]xmlProgramme{{Channel: "s1ch1", Title: "S1 Show"}},
	)
	src2Body := xmltvSource(
		[]xmlChannel{{ID: "s2ch1", DisplayName: "Server2 Ch1"}},
		[]xmlProgramme{{Channel: "s2ch1", Title: "S2 Show"}},
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/epg1.xml":
			io.WriteString(w, src1Body)
		case "/epg2.xml":
			io.WriteString(w, src2Body)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	t.Setenv("EPG_URL_1", srv.URL+"/epg1.xml")
	t.Setenv("EPG_URL_2", srv.URL+"/epg2.xml")
	utils.ResetCaches()

	p := NewProcessor(logger.Default)
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if _, err := os.Stat(config.GetEPGPath()); err != nil {
		t.Fatalf("merged EPG file not created: %v", err)
	}

	doc := parseMergedXML(t, config.GetEPGPath())
	if len(doc.Channels) != 2 {
		t.Errorf("expected 2 channels, got %d", len(doc.Channels))
	}
	if len(doc.Programmes) != 2 {
		t.Errorf("expected 2 programmes, got %d", len(doc.Programmes))
	}
}

// TestProcessor_Run_GzippedSource verifies that a source served as a
// gzip-compressed file (Content-Type: application/gzip, URL ending in .gz)
// is transparently decompressed before being merged.
func TestProcessor_Run_GzippedSource(t *testing.T) {
	setupTestConfig(t)

	plainBody := xmltvSource(
		[]xmlChannel{{ID: "gz1", DisplayName: "Gzip Channel"}},
		[]xmlProgramme{{Channel: "gz1", Title: "Gzip Show"}},
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/epg.xml.gz":
			w.Header().Set("Content-Type", "application/gzip")
			w.Write(gzipBytes(t, plainBody))
		case "/epg_ct.xml":
			// Content-Type signals gzip even though URL doesn't end in .gz
			w.Header().Set("Content-Type", "application/x-gzip")
			w.Write(gzipBytes(t, plainBody))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tests := []struct {
		name string
		path string
	}{
		{"gz URL extension", "/epg.xml.gz"},
		{"application/x-gzip content-type", "/epg_ct.xml"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setupTestConfig(t)
			t.Setenv("EPG_URL_1", srv.URL+tc.path)
			utils.ResetCaches()

			p := NewProcessor(logger.Default)
			if err := p.Run(context.Background()); err != nil {
				t.Fatalf("Run: %v", err)
			}

			doc := parseMergedXML(t, config.GetEPGPath())
			if len(doc.Channels) != 1 || doc.Channels[0].ID != "gz1" {
				t.Errorf("unexpected channels: %+v", doc.Channels)
			}
			if len(doc.Programmes) != 1 || doc.Programmes[0].Title != "Gzip Show" {
				t.Errorf("unexpected programmes: %+v", doc.Programmes)
			}
		})
	}
}

// TestDecompressIfNeeded_PlainPassthrough ensures plain XML responses are not
// touched by decompressIfNeeded.
func TestDecompressIfNeeded_PlainPassthrough(t *testing.T) {
	body := io.NopCloser(strings.NewReader("<tv></tv>"))
	resp := &http.Response{
		Header: http.Header{"Content-Type": []string{"application/xml"}},
		Body:   body,
	}
	rc, err := decompressIfNeeded(resp, "http://example.com/epg.xml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rc != body {
		t.Error("expected plain body to be returned unchanged")
	}
}

// TestDecompressIfNeeded_GzipByURL ensures a .gz URL triggers decompression.
func TestDecompressIfNeeded_GzipByURL(t *testing.T) {
	data := xmltvSource(
		[]xmlChannel{{ID: "c1", DisplayName: "C1"}}, nil,
	)
	compressed := gzipBytes(t, data)

	resp := &http.Response{
		Header: http.Header{"Content-Type": []string{"application/octet-stream"}},
		Body:   io.NopCloser(bytes.NewReader(compressed)),
	}
	rc, err := decompressIfNeeded(resp, "http://example.com/epg.xml.gz")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(got), "C1") {
		t.Errorf("decompressed content missing expected data, got: %s", got)
	}
}

// TestProcessor_Run_DecompressionBombRejected verifies that a gzip-compressed
// source whose decompressed size exceeds the size limit is rejected rather than
// written to disk, preventing a decompression-bomb DoS.
func TestProcessor_Run_DecompressionBombRejected(t *testing.T) {
	setupTestConfig(t)

	// Override the package-level limit to 1 byte so any real payload triggers it.
	origMax := maxEPGBytes
	maxEPGBytes = 1
	t.Cleanup(func() { maxEPGBytes = origMax })

	bigBody := xmltvSource(
		[]xmlChannel{{ID: "bomb", DisplayName: "Bomb"}},
		[]xmlProgramme{{Channel: "bomb", Title: "Boom"}},
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		w.Write(gzipBytes(t, bigBody))
	}))
	defer srv.Close()

	t.Setenv("EPG_URL_1", srv.URL+"/epg.xml.gz")
	utils.ResetCaches()

	p := NewProcessor(logger.Default)
	err := p.Run(context.Background())

	// With only one source and no cached fallback, Run must return an error.
	if err == nil {
		t.Error("expected Run to fail when decompressed size exceeds limit, got nil")
	}

	// The merged EPG file must not have been created.
	if _, statErr := os.Stat(config.GetEPGPath()); statErr == nil {
		t.Error("merged EPG file should not exist after a bomb rejection")
	}
}

// ---------------------------------------------------------------------------
// Channel remapping tests
// ---------------------------------------------------------------------------

// TestMergeXMLTV_ChannelRemapping verifies that an EPG channel id is rewritten
// to the mapped M3U tvg-id in both channel and programme elements.
func TestMergeXMLTV_ChannelRemapping(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.xml")
	out := filepath.Join(dir, "out.xml")

	os.WriteFile(src, []byte(xmltvSource(
		[]xmlChannel{{ID: "epg.channel.id", DisplayName: "Remapped Channel"}},
		[]xmlProgramme{{Channel: "epg.channel.id", Title: "Remapped Show"}},
	)), 0644)

	channelMap := map[string]string{"epg.channel.id": "m3u.tvg.id"}
	tvgIDs := map[string]struct{}{"m3u.tvg.id": {}}

	if err := mergeXMLTV([]string{src}, out, tvgIDs, channelMap); err != nil {
		t.Fatalf("mergeXMLTV: %v", err)
	}

	doc := parseMergedXML(t, out)
	if len(doc.Channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(doc.Channels))
	}
	if doc.Channels[0].ID != "m3u.tvg.id" {
		t.Errorf("channel id not remapped: got %q, want %q", doc.Channels[0].ID, "m3u.tvg.id")
	}
	if len(doc.Programmes) != 1 {
		t.Fatalf("expected 1 programme, got %d", len(doc.Programmes))
	}
	if doc.Programmes[0].Channel != "m3u.tvg.id" {
		t.Errorf("programme channel not remapped: got %q, want %q", doc.Programmes[0].Channel, "m3u.tvg.id")
	}
}

// TestMergeXMLTV_RemappingWithoutFilter verifies that remapping works when
// tvgIDs is nil (keep-all mode): the attribute is rewritten but nothing is dropped.
func TestMergeXMLTV_RemappingWithoutFilter(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.xml")
	out := filepath.Join(dir, "out.xml")

	os.WriteFile(src, []byte(xmltvSource(
		[]xmlChannel{
			{ID: "epg.a", DisplayName: "A"},
			{ID: "epg.b", DisplayName: "B"},
		},
		[]xmlProgramme{
			{Channel: "epg.a", Title: "Show A"},
			{Channel: "epg.b", Title: "Show B"},
		},
	)), 0644)

	channelMap := map[string]string{"epg.a": "tvg.a"} // only remap a
	if err := mergeXMLTV([]string{src}, out, nil, channelMap); err != nil {
		t.Fatalf("mergeXMLTV: %v", err)
	}

	doc := parseMergedXML(t, out)
	if len(doc.Channels) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(doc.Channels))
	}
	ids := map[string]bool{}
	for _, ch := range doc.Channels {
		ids[ch.ID] = true
	}
	if !ids["tvg.a"] {
		t.Error("expected 'tvg.a' in channels after remap")
	}
	if !ids["epg.b"] {
		t.Error("expected 'epg.b' unchanged in channels")
	}
}

// TestMergeXMLTV_RemapEnablesFilterPass verifies that an EPG channel whose
// original id is NOT in tvgIDs but whose remapped id IS in tvgIDs passes
// through the filter and is rewritten.
func TestMergeXMLTV_RemapEnablesFilterPass(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.xml")
	out := filepath.Join(dir, "out.xml")

	os.WriteFile(src, []byte(xmltvSource(
		[]xmlChannel{
			{ID: "epg.old", DisplayName: "Old EPG ID"},
			{ID: "epg.keep", DisplayName: "Already matching"},
		},
		[]xmlProgramme{
			{Channel: "epg.old", Title: "Old Show"},
			{Channel: "epg.keep", Title: "Keep Show"},
		},
	)), 0644)

	// tvgIDs has the M3U ids; epg.old is not there but maps to m3u.new which is.
	tvgIDs := map[string]struct{}{
		"m3u.new":  {},
		"epg.keep": {},
	}
	channelMap := map[string]string{"epg.old": "m3u.new"}

	if err := mergeXMLTV([]string{src}, out, tvgIDs, channelMap); err != nil {
		t.Fatalf("mergeXMLTV: %v", err)
	}

	doc := parseMergedXML(t, out)
	if len(doc.Channels) != 2 {
		t.Fatalf("expected 2 channels, got %d: %+v", len(doc.Channels), doc.Channels)
	}
	ids := map[string]bool{}
	for _, ch := range doc.Channels {
		ids[ch.ID] = true
	}
	if !ids["m3u.new"] {
		t.Errorf("expected remapped channel 'm3u.new', got channels: %+v", doc.Channels)
	}
	if !ids["epg.keep"] {
		t.Errorf("expected passthrough channel 'epg.keep', got channels: %+v", doc.Channels)
	}
	if len(doc.Programmes) != 2 {
		t.Errorf("expected 2 programmes, got %d", len(doc.Programmes))
	}
}

// TestGetEPGChannelMappings verifies that EPG_CHANNEL_MAP_X env vars are
// parsed correctly into the epgID → tvgID map.
func TestGetEPGChannelMappings(t *testing.T) {
	setupTestConfig(t)

	t.Setenv("EPG_CHANNEL_MAP_1", "my.tvg.id=their.epg.id")
	t.Setenv("EPG_CHANNEL_MAP_2", "another.tvg=another.epg")
	utils.ResetCaches()
	t.Cleanup(utils.ResetCaches)

	m := utils.GetEPGChannelMappings()
	if got := m["their.epg.id"]; got != "my.tvg.id" {
		t.Errorf("EPG_CHANNEL_MAP_1: got %q, want %q", got, "my.tvg.id")
	}
	if got := m["another.epg"]; got != "another.tvg" {
		t.Errorf("EPG_CHANNEL_MAP_2: got %q, want %q", got, "another.tvg")
	}
}
