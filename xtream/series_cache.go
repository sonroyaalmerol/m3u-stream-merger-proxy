package xtream

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

var fragMu sync.Mutex

// AppendSeriesFragment appends entries under the lock shared with the
// background populate loop. Replay reads last-wins per ID, so appending is
// semantically a replace without rewriting the whole cache.
func AppendSeriesFragment(path string, entries []FragmentEntry) error {
	fragMu.Lock()
	defer fragMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if err := writeEntries(bufio.NewWriter(f), entries); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

type fragmentSpan struct {
	offset int64
	length int64
}

func indexSeriesFragment(path string) (*os.File, map[uint64]fragmentSpan, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	fail := func(err error) (*os.File, map[uint64]fragmentSpan, error) {
		_ = f.Close()
		return nil, nil, err
	}

	spans := make(map[uint64]fragmentSpan)
	r := bufio.NewReader(f)
	var offset, bodyStart int64
	var currentID uint64
	haveCurrent := false
	atLineStart := true
	for {
		partStart := offset
		part, readErr := r.ReadSlice('\n')
		offset += int64(len(part))
		if atLineStart {
			line := bytes.TrimSuffix(part, []byte{'\n'})
			if len(line) > 9 && bytes.HasPrefix(line, []byte("#XSERIES ")) {
				if errors.Is(readErr, bufio.ErrBufferFull) {
					return fail(fmt.Errorf("fragment header too long at %d", partStart))
				}
				id, parseErr := strconv.ParseUint(string(line[9:]), 10, 64)
				if parseErr != nil {
					return fail(fmt.Errorf("bad fragment header %q", line))
				}
				if haveCurrent {
					spans[currentID] = fragmentSpan{offset: bodyStart, length: partStart - bodyStart}
				}
				currentID = id
				bodyStart = offset
				haveCurrent = true
			}
		}
		atLineStart = !errors.Is(readErr, bufio.ErrBufferFull)
		if readErr == nil || errors.Is(readErr, bufio.ErrBufferFull) {
			continue
		}
		if !errors.Is(readErr, io.EOF) {
			return fail(readErr)
		}
		break
	}
	if haveCurrent {
		spans[currentID] = fragmentSpan{offset: bodyStart, length: offset - bodyStart}
	}
	return f, spans, nil
}

func replaySeriesFragment(path string, stubs []SeriesStub, emit func(string) error) (int, error) {
	f, spans, err := indexSeriesFragment(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()

	replayed := 0
	for _, stub := range stubs {
		span, ok := spans[stub.UpstreamID]
		if !ok {
			continue
		}
		r := bufio.NewReader(io.NewSectionReader(f, span.offset, span.length))
		for {
			line, readErr := r.ReadString('\n')
			line = strings.TrimSuffix(line, "\n")
			if line != "" {
				if err := emit(line); err != nil {
					return replayed, err
				}
				replayed++
			}
			if readErr == nil {
				continue
			}
			if !errors.Is(readErr, io.EOF) {
				return replayed, readErr
			}
			break
		}
	}
	return replayed, nil
}

// CompactSeriesFragment keeps only the last version of each series (and only
// IDs present in valid, when non-nil), rewriting the file atomically. Runs
// once per populate pass, not per batch.
func CompactSeriesFragment(path string, valid map[uint64]struct{}) error {
	fragMu.Lock()
	defer fragMu.Unlock()
	src, spans, _ := indexSeriesFragment(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if src != nil {
		defer func() { _ = src.Close() }()
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for id, span := range spans {
		if valid != nil {
			if _, ok := valid[id]; !ok {
				continue
			}
		}
		if _, err := fmt.Fprintf(w, "#XSERIES %d\n", id); err != nil {
			_ = f.Close()
			return err
		}
		if _, err := io.CopyN(w, io.NewSectionReader(src, span.offset, span.length), span.length); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func writeEntries(w *bufio.Writer, entries []FragmentEntry) error {
	for _, e := range entries {
		if _, err := fmt.Fprintf(w, "#XSERIES %d\n", e.UpstreamID); err != nil {
			return err
		}
		for _, l := range e.Lines {
			if _, err := w.WriteString(l); err != nil {
				return err
			}
			if err := w.WriteByte('\n'); err != nil {
				return err
			}
		}
	}
	return w.Flush()
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
