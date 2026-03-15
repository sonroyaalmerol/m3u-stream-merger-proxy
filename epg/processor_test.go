package epg

import (
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
	XMLName    xml.Name      `xml:"tv"`
	Channels   []xmlChannel  `xml:"channel"`
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

// setupTestConfig sets data and temp paths to temporary directories so tests
// don't touch the real /m3u-proxy/data path.
func setupTestConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	config.SetConfig(&config.Config{
		DataPath: filepath.Join(dir, "data") + "/",
		TempPath: filepath.Join(dir, "tmp") + "/",
	})
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

	if err := mergeXMLTV([]string{src1, src2}, out); err != nil {
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
		[]xmlChannel{{ID: "dup", DisplayName: "First"}},
		nil,
	)), 0644)
	os.WriteFile(src2, []byte(xmltvSource(
		[]xmlChannel{{ID: "dup", DisplayName: "Second"}},
		nil,
	)), 0644)

	if err := mergeXMLTV([]string{src1, src2}, out); err != nil {
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

	if err := mergeXMLTV([]string{src1, src2}, out); err != nil {
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
// two XMLTV files and verifies the processor produces a merged output.
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

	// Reset cached index list so Setenv is picked up.
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
