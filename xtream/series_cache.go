package xtream

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// SeriesStub is one get_series entry cached at ingest for lazy episode resolution.
type SeriesStub struct {
	UpstreamID uint64
	Name       string
	Group      string
	Cover      string
}

// FragmentEntry holds the rendered M3U lines for one upstream series.
type FragmentEntry struct {
	UpstreamID uint64
	Lines      []string
}

var stubMagic = [8]byte{'X', 'S', 'T', 'U', 'B', '0', '1', 0}

var le = binary.LittleEndian

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func putStr(dst []byte, s string) []byte {
	dst = append(dst, byte(len(s)>>8), byte(len(s)))
	return append(dst, s...)
}

func strAt(b []byte, off int) (string, int, error) {
	if off+2 > len(b) {
		return "", 0, fmt.Errorf("stub truncated at %d", off)
	}
	n := int(b[off])<<8 | int(b[off+1])
	off += 2
	if off+n > len(b) {
		return "", 0, fmt.Errorf("stub string overruns at %d", off)
	}
	return string(b[off : off+n]), off + n, nil
}

func WriteSeriesStubs(path string, stubs []SeriesStub) error {
	dst := make([]byte, 0, 96*len(stubs)+16)
	dst = append(dst, stubMagic[:]...)
	dst = append(dst, byte(len(stubs)), byte(len(stubs)>>8))
	for _, s := range stubs {
		var hdr [8]byte
		le.PutUint64(hdr[:], s.UpstreamID)
		dst = append(dst, hdr[:]...)
		dst = putStr(dst, s.Name)
		dst = putStr(dst, s.Group)
		dst = putStr(dst, s.Cover)
	}
	return writeAtomic(path, dst)
}

func ReadSeriesStubs(path string) ([]SeriesStub, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) < 10 || !bytes.Equal(b[:8], stubMagic[:]) {
		return nil, fmt.Errorf("bad stub magic in %s", path)
	}
	count := int(b[8]) | int(b[9])<<8
	off := 10
	stubs := make([]SeriesStub, 0, min(count, 1<<16))
	for range count {
		if off+8 > len(b) {
			return nil, fmt.Errorf("stub truncated at %d", off)
		}
		s := SeriesStub{UpstreamID: le.Uint64(b[off:])}
		off += 8
		if s.Name, off, err = strAt(b, off); err != nil {
			return nil, err
		}
		if s.Group, off, err = strAt(b, off); err != nil {
			return nil, err
		}
		if s.Cover, off, err = strAt(b, off); err != nil {
			return nil, err
		}
		stubs = append(stubs, s)
	}
	return stubs, nil
}

func WriteSeriesFragment(path string, entries []FragmentEntry) error {
	var buf bytes.Buffer
	for _, e := range entries {
		buf.WriteString("#XSERIES ")
		buf.WriteString(strconv.FormatUint(e.UpstreamID, 10))
		buf.WriteByte('\n')
		for _, l := range e.Lines {
			buf.WriteString(l)
			buf.WriteByte('\n')
		}
	}
	return writeAtomic(path, buf.Bytes())
}

func ReadSeriesFragment(path string) ([]FragmentEntry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var entries []FragmentEntry
	cur := -1
	for len(b) > 0 {
		nl := bytes.IndexByte(b, '\n')
		var line []byte
		if nl < 0 {
			line = b
			b = nil
		} else {
			line = b[:nl]
			b = b[nl+1:]
		}
		if len(line) > 9 && bytes.HasPrefix(line, []byte("#XSERIES ")) {
			id, err := strconv.ParseUint(string(line[9:]), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("bad fragment header %q", line)
			}
			entries = append(entries, FragmentEntry{UpstreamID: id})
			cur = len(entries) - 1
			continue
		}
		if len(line) == 0 || cur < 0 {
			continue
		}
		entries[cur].Lines = append(entries[cur].Lines, string(line))
	}
	return entries, nil
}
