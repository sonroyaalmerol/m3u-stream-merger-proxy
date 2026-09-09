package sourceproc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func buildStoreReserved(tb testing.TB, n, reserve int) {
	tb.Helper()
	w, err := NewStreamStoreWriter()
	if err != nil {
		tb.Fatal(err)
	}
	w.reserve(reserve)
	for i := range n {
		s := storeStream(i)
		key, _ := slugParts(s.Title)
		if err := w.Add(key, s); err != nil {
			tb.Fatal(err)
		}
	}
	if err := w.Commit(); err != nil {
		tb.Fatal(err)
	}
}

func assertRoundTrip(tb testing.TB, ids []int) {
	tb.Helper()
	for _, i := range ids {
		want := storeStream(i)
		got, err := defaultStore.Get(EncodeSlug(want))
		if err != nil {
			tb.Fatalf("stream %d: %v", i, err)
		}
		if got.Title != want.Title || got.TvgID != want.TvgID || len(got.URLs) != 1 {
			tb.Fatalf("stream %d: got %+v", i, got)
		}
		if got.URLs[0].URL != want.URLs[0].URL {
			tb.Fatalf("stream %d url: got %q want %q", i, got.URLs[0].URL, want.URLs[0].URL)
		}
	}
}

func buildFiles(tb testing.TB) []string {
	tb.Helper()
	entries, err := os.ReadDir(storeDir())
	if err != nil {
		tb.Fatal(err)
	}
	var found []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".build") {
			found = append(found, entry.Name())
		}
	}
	return found
}

func TestMappedIndexRoundTrip(t *testing.T) {
	benchDataDir(t)
	buildStoreReserved(t, 5000, 5000)

	assertRoundTrip(t, []int{0, 1, 2499, 4999})
	if _, err := defaultStore.Get(EncodeSlug(storeStream(999999))); err == nil {
		t.Fatal("expected miss for unknown slug")
	}
	if left := buildFiles(t); len(left) != 0 {
		t.Fatalf("build files left after commit: %v", left)
	}
}

func TestMappedIndexOverflowFallsBackToHeap(t *testing.T) {
	benchDataDir(t)
	buildStoreReserved(t, 500, 10)

	assertRoundTrip(t, []int{0, 9, 10, 499})
	if left := buildFiles(t); len(left) != 0 {
		t.Fatalf("build files left after commit: %v", left)
	}
}

func TestMappedIndexDiscardReleases(t *testing.T) {
	benchDataDir(t)
	w, err := NewStreamStoreWriter()
	if err != nil {
		t.Fatal(err)
	}
	w.reserve(1000)
	if got := buildFiles(t); len(got) == 0 {
		t.Skip("mapping unavailable on this filesystem; heap fallback in use")
	}
	s := storeStream(1)
	key, _ := slugParts(s.Title)
	if err := w.Add(key, s); err != nil {
		t.Fatal(err)
	}
	w.Discard()

	if left := buildFiles(t); len(left) != 0 {
		t.Fatalf("build files left after discard: %v", left)
	}
}

func TestRemoveStaleBuildFiles(t *testing.T) {
	benchDataDir(t)
	if err := os.MkdirAll(storeDir(), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(storeDir(), "g7.slug.build")
	if err := os.WriteFile(stale, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStreamStoreWriter(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale build file survived: %v", err)
	}
}

func TestMapSliceWritesSurviveAndRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "probe.build")
	entries, m := mapSlice[lookupEntry](path, 4)
	if m == nil {
		t.Skip("mapping unavailable on this filesystem")
	}
	if cap(entries) != 4 || len(entries) != 0 {
		t.Fatalf("got len=%d cap=%d, want len=0 cap=4", len(entries), cap(entries))
	}
	for i := range 4 {
		entries = append(entries, lookupEntry{key: uint64(i * 7), recordID: uint32(i)})
	}
	for i, entry := range entries {
		if entry.key != uint64(i*7) || entry.recordID != uint32(i) {
			t.Fatalf("entry %d: got %+v", i, entry)
		}
	}
	m.release()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("mapping file survived release: %v", err)
	}
	m.release()
}
