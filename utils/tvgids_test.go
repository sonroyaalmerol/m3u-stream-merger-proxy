package utils

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTvgIDFilterRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tvgids.bin")
	hashes := []uint64{TvgIDHash("b.tv"), TvgIDHash("a.tv"), TvgIDHash("b.tv")}
	if err := WriteTvgIDHashes(path, hashes); err != nil {
		t.Fatal(err)
	}

	filter := LoadTvgIDFilter(path)
	if len(filter) != 2 {
		t.Fatalf("want 2 deduped entries, got %d", len(filter))
	}
	for _, id := range []string{"a.tv", "b.tv"} {
		if !filter.Has(id) {
			t.Fatalf("%s missing from filter", id)
		}
	}
	if filter.Has("c.tv") {
		t.Fatal("c.tv should not match")
	}
}

func TestTvgIDFilterNilKeepsEverything(t *testing.T) {
	dir := t.TempDir()
	if LoadTvgIDFilter(filepath.Join(dir, "missing.bin")) != nil {
		t.Fatal("missing file should disable filtering")
	}

	legacy := filepath.Join(dir, "legacy.txt")
	if err := os.WriteFile(legacy, []byte("cnn.us\nbbc.news\n"), 0644); err != nil {
		t.Fatal(err)
	}
	filter := LoadTvgIDFilter(legacy)
	if filter != nil {
		t.Fatal("legacy text file should be rejected")
	}
	if !filter.Has("anything") {
		t.Fatal("nil filter must match everything")
	}
}
