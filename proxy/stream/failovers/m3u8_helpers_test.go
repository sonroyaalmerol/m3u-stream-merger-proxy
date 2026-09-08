package failovers

import (
	"strings"
	"testing"
)

func TestDecodeSlugRoundTrip(t *testing.T) {
	want := &M3U8Segment{URL: "https://example.com/live/segment.ts", SourceM3U: "1"}

	got, err := decodeSlug(encodeSlug(want))
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != want.URL || got.SourceM3U != want.SourceM3U {
		t.Fatalf("decoded segment = %#v, want %#v", got, want)
	}
}

func TestDecodeSlugRejectsOversizedOutput(t *testing.T) {
	slug := encodeSlug(&M3U8Segment{URL: strings.Repeat("a", maxSegmentSlugBytes)})

	if _, err := decodeSlug(slug); err == nil {
		t.Fatal("decodeSlug accepted oversized decompressed data")
	}
}
