package sourceproc

import (
	"bufio"
	"cmp"
	"container/heap"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"

	"m3u-stream-merger/config"

	"github.com/cespare/xxhash"
	"github.com/goccy/go-json"
)

// spillPartitions bounds phase-2 memory to corpus/spillPartitions per fold pass.
// ponytail: fixed 256 fits 1M streams at ~8MB slices per partition; raise for extreme scale.
const spillPartitions = 256

type spillPart struct {
	mu  sync.Mutex
	buf *bufio.Writer
	f   *os.File
}

// spillSorter is an external hash-partitioned sort: entries spill to partition
// files during parse, partitions fold and render in parallel, one k-way merge streams the ordered result.
type spillSorter struct {
	sortingKey string
	sortingDir string
	err        error
	parts      []*spillPart
}

func newSpillSorter() *spillSorter {
	_ = os.RemoveAll(config.GetSortDirPath())
	if err := os.MkdirAll(config.GetSortDirPath(), 0755); err != nil {
		return &spillSorter{err: err}
	}

	parts := make([]*spillPart, spillPartitions)
	for i := range parts {
		parts[i] = &spillPart{}
	}

	return &spillSorter{
		sortingKey: os.Getenv("SORTING_KEY"),
		sortingDir: strings.ToLower(os.Getenv("SORTING_DIRECTION")),
		parts:      parts,
	}
}

func partitionPath(i int) string {
	return filepath.Join(config.GetSortDirPath(), fmt.Sprintf("p%03d.bin", i))
}

// Add marshals the stream into its hash partition; same-title entries co-locate so fold semantics match one in-memory map.
func (s *spillSorter) Add(stream *StreamInfo) error {
	if s.err != nil {
		return s.err
	}

	data, err := json.Marshal(stream)
	if err != nil {
		return err
	}

	idx := xxhash.Sum64String(sanitizeField(stream.Title)) % spillPartitions
	p := s.parts[idx]
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.buf == nil {
		f, err := os.Create(partitionPath(int(idx)))
		if err != nil {
			return err
		}
		p.f = f
		p.buf = bufio.NewWriterSize(f, 1<<16)
	}

	var lenBuf [4]byte
	binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(data)))
	if _, err := p.buf.Write(lenBuf[:]); err != nil {
		return err
	}
	_, err = p.buf.Write(data)

	return err
}

// renderedEntry is the pre-rendered per-entry payload; storeKey rides along for the ordered store index.
type renderedEntry struct {
	storeKey uint64
	m3u      string
	storeRec []byte
	tvgID    string
}

// MergeRendered folds duplicate titles per partition, sorts, renders in parallel, streams one ordered merge through emit.
func (s *spillSorter) MergeRendered(render func(*StreamInfo) renderedEntry, emit func(renderedEntry) error) error {
	if s.err != nil {
		return s.err
	}

	var partPaths []string
	for i, p := range s.parts {
		p.mu.Lock()
		if p.buf != nil {
			if err := p.buf.Flush(); err != nil {
				p.mu.Unlock()
				return err
			}
			if err := p.f.Close(); err != nil {
				p.mu.Unlock()
				return err
			}
			partPaths = append(partPaths, partitionPath(i))
		}
		p.mu.Unlock()
	}

	var (
		mu       sync.Mutex
		firstErr error
		runPaths []string
		wg       sync.WaitGroup
	)
	sem := make(chan struct{}, max(1, runtime.GOMAXPROCS(0)))
	for _, path := range partPaths {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()

			runPath, err := s.buildRun(path, render)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			runPaths = append(runPaths, runPath)
		})
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}

	readers := make([]*runReader, 0, len(runPaths))
	defer func() {
		for _, r := range readers {
			_ = r.f.Close()
		}
	}()
	for _, path := range runPaths {
		r, err := openRun(path)
		if err != nil {
			return err
		}
		if err := r.next(); err != nil {
			return err
		}
		if r.ok {
			readers = append(readers, r)
		} else {
			_ = r.f.Close()
		}
	}

	h := &runHeap{rs: readers, desc: s.sortingDir == "desc"}
	heap.Init(h)
	for h.Len() > 0 {
		r := h.rs[0]
		if err := emit(r.cur.re); err != nil {
			return err
		}
		if err := r.next(); err != nil {
			return err
		}
		if r.ok {
			heap.Fix(h, 0)
		} else {
			heap.Pop(h)
		}
	}

	return nil
}

// buildRun loads one partition, folds duplicate titles (same semantics as an in-memory map), sorts, writes the rendered run.
func (s *spillSorter) buildRun(partPath string, render func(*StreamInfo) renderedEntry) (string, error) {
	f, err := os.Open(partPath)
	if err != nil {
		return "", err
	}

	folded := make(map[string]*StreamInfo)
	br := bufio.NewReaderSize(f, 1<<16)
	var lenBuf [4]byte
	for {
		if _, err := io.ReadFull(br, lenBuf[:]); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			_ = f.Close()
			return "", err
		}
		buf := make([]byte, binary.LittleEndian.Uint32(lenBuf[:]))
		if _, err := io.ReadFull(br, buf); err != nil {
			_ = f.Close()
			return "", err
		}
		var info StreamInfo
		if err := json.Unmarshal(buf, &info); err != nil {
			continue
		}
		key := sanitizeField(info.Title)
		if old, ok := folded[key]; ok {
			mergeStreamInfoAttributes(old, &info)
		} else {
			folded[key] = &info
		}
	}
	_ = f.Close()
	_ = os.Remove(partPath)

	entries := make([]sortEntry, 0, len(folded))
	for _, st := range folded {
		entries = append(entries, sortEntryFor(st, s.sortingKey))
	}
	slices.SortFunc(entries, func(a, b sortEntry) int {
		c := compareSortEntries(a, b)
		if s.sortingDir == "desc" {
			return -c
		}
		return c
	})

	runPath := partPath + ".run"
	rf, err := os.Create(runPath)
	if err != nil {
		return "", err
	}
	fw := &frameWriter{w: bufio.NewWriterSize(rf, 1<<16)}
	for _, e := range entries {
		if err := writeRunRecord(fw, e, render(e.stream)); err != nil {
			_ = rf.Close()
			return "", err
		}
	}
	if err := fw.w.Flush(); err != nil {
		_ = rf.Close()
		return "", err
	}
	if err := rf.Close(); err != nil {
		return "", err
	}

	return runPath, nil
}

func (s *spillSorter) Close() {
	for _, p := range s.parts {
		p.mu.Lock()
		if p.buf != nil {
			_ = p.buf.Flush()
			_ = p.f.Close()
			p.buf = nil
			p.f = nil
		}
		p.mu.Unlock()
	}
	_ = os.RemoveAll(config.GetSortDirPath())
}

type sortEntry struct {
	stream *StreamInfo
	title  string
	key    string
	num    int
	numOK  bool
}

func sortEntryFor(s *StreamInfo, sortingKey string) sortEntry {
	e := sortEntry{stream: s, title: s.Title}
	numeric := false

	switch sortingKey {
	case "tvg-chno", "channel-id", "channel-number":
		e.key, numeric = s.TvgChNo, true
	case "tvg-id":
		e.key, numeric = s.TvgID, true
	case "source":
		e.key, numeric = s.SourceM3U, true
	case "tvg-group", "group-title":
		e.key = strings.ToLower(s.Group)
	case "tvg-type":
		e.key = strings.ToLower(s.TvgType)
	default:
		e.key = strings.ToLower(s.Title)
	}

	if numeric {
		if n, err := strconv.Atoi(e.key); err == nil {
			e.num, e.numOK = n, true
		}
	}

	return e
}

func compareSortEntries(a, b sortEntry) int {
	if a.numOK && b.numOK {
		if c := cmp.Compare(a.num, b.num); c != 0 {
			return c
		}
	} else if c := strings.Compare(a.key, b.key); c != 0 {
		return c
	}

	return strings.Compare(a.title, b.title)
}

func mergeStreamInfoAttributes(base, new *StreamInfo) *StreamInfo {
	if base.Title == "" {
		base.Title = new.Title
	}
	if base.TvgID == "" {
		base.TvgID = new.TvgID
	}
	if base.TvgChNo == "" {
		base.TvgChNo = new.TvgChNo
	}
	if base.TvgType == "" {
		base.TvgType = new.TvgType
	}
	if base.LogoURL == "" {
		base.LogoURL = new.LogoURL
	}
	if base.Group == "" {
		base.Group = new.Group
	}

	for _, u := range new.URLs {
		base.AddURL(u.M3UIndex, u.LineNum, u.URL)
	}

	if new.SourceM3U < base.SourceM3U || (new.SourceM3U == base.SourceM3U && new.SourceIndex < base.SourceIndex) {
		base.SourceM3U = new.SourceM3U
		base.SourceIndex = new.SourceIndex
	}

	return base
}

type runEntry struct {
	se sortEntry
	re renderedEntry
}

type runReader struct {
	f   *os.File
	br  *bufio.Reader
	tmp [12]byte
	cur runEntry
	ok  bool
}

func openRun(path string) (*runReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	return &runReader{f: f, br: bufio.NewReaderSize(f, 1<<16)}, nil
}

func (r *runReader) readU32() (uint32, error) {
	if _, err := io.ReadFull(r.br, r.tmp[:4]); err != nil {
		return 0, err
	}

	return binary.LittleEndian.Uint32(r.tmp[:4]), nil
}

func (r *runReader) readStr(n uint32) (string, error) {
	buf := make([]byte, n)
	if _, err := io.ReadFull(r.br, buf); err != nil {
		return "", err
	}

	return string(buf), nil
}

func (r *runReader) next() error {
	keyLen, err := r.readU32()
	if err != nil {
		if errors.Is(err, io.EOF) {
			r.ok = false
			return nil
		}
		return err
	}

	e := runEntry{}
	e.se.key, err = r.readStr(keyLen)
	if err != nil {
		return err
	}
	if _, err := io.ReadFull(r.br, r.tmp[:4]); err != nil {
		return err
	}
	num := int(int32(binary.LittleEndian.Uint32(r.tmp[:4])))
	if _, err := io.ReadFull(r.br, r.tmp[:1]); err != nil {
		return err
	}
	numOK := r.tmp[0] == 1
	titleLen, err := r.readU32()
	if err != nil {
		return err
	}
	e.se.num, e.se.numOK = num, numOK
	e.se.title, err = r.readStr(titleLen)
	if err != nil {
		return err
	}

	if _, err := io.ReadFull(r.br, r.tmp[:8]); err != nil {
		return err
	}
	e.re.storeKey = binary.LittleEndian.Uint64(r.tmp[:8])

	m3uLen, err := r.readU32()
	if err != nil {
		return err
	}
	e.re.m3u, err = r.readStr(m3uLen)
	if err != nil {
		return err
	}
	recLen, err := r.readU32()
	if err != nil {
		return err
	}
	if recLen > 0 {
		buf := make([]byte, recLen)
		if _, err := io.ReadFull(r.br, buf); err != nil {
			return err
		}
		e.re.storeRec = buf
	}
	tvgLen, err := r.readU32()
	if err != nil {
		return err
	}
	e.re.tvgID, err = r.readStr(tvgLen)
	if err != nil {
		return err
	}

	r.cur = e
	r.ok = true

	return nil
}

type runHeap struct {
	rs   []*runReader
	desc bool
}

func (h *runHeap) Len() int { return len(h.rs) }
func (h *runHeap) Less(i, j int) bool {
	c := compareSortEntries(h.rs[i].cur.se, h.rs[j].cur.se)
	if h.desc {
		return c > 0
	}
	return c < 0
}
func (h *runHeap) Swap(i, j int) { h.rs[i], h.rs[j] = h.rs[j], h.rs[i] }
func (h *runHeap) Push(x any)    { h.rs = append(h.rs, x.(*runReader)) }
func (h *runHeap) Pop() any {
	old := h.rs
	n := len(old)
	x := old[n-1]
	h.rs = old[:n-1]
	return x
}

type frameWriter struct {
	w   *bufio.Writer
	tmp [12]byte
}

func (fw *frameWriter) u8(v byte) error {
	fw.tmp[0] = v
	_, err := fw.w.Write(fw.tmp[:1])
	return err
}

func (fw *frameWriter) u32(v uint32) error {
	binary.LittleEndian.PutUint32(fw.tmp[:4], v)
	_, err := fw.w.Write(fw.tmp[:4])
	return err
}

func (fw *frameWriter) u64(v uint64) error {
	binary.LittleEndian.PutUint64(fw.tmp[:8], v)
	_, err := fw.w.Write(fw.tmp[:8])
	return err
}

func (fw *frameWriter) bytes(b []byte) error {
	_, err := fw.w.Write(b)
	return err
}

func (fw *frameWriter) str(s string) error {
	_, err := fw.w.WriteString(s)
	return err
}

// writeRunRecord frames one rendered entry: sort key first (merge heap never decodes payloads), then pre-rendered bytes.
func writeRunRecord(fw *frameWriter, e sortEntry, re renderedEntry) error {
	if err := fw.u32(uint32(len(e.key))); err != nil {
		return err
	}
	if err := fw.str(e.key); err != nil {
		return err
	}
	if err := fw.u32(uint32(e.num)); err != nil {
		return err
	}
	numOK := byte(0)
	if e.numOK {
		numOK = 1
	}
	if err := fw.u8(numOK); err != nil {
		return err
	}
	if err := fw.u32(uint32(len(e.title))); err != nil {
		return err
	}
	if err := fw.str(e.title); err != nil {
		return err
	}

	if err := fw.u64(re.storeKey); err != nil {
		return err
	}
	if err := fw.u32(uint32(len(re.m3u))); err != nil {
		return err
	}
	if err := fw.str(re.m3u); err != nil {
		return err
	}
	if err := fw.u32(uint32(len(re.storeRec))); err != nil {
		return err
	}
	if err := fw.bytes(re.storeRec); err != nil {
		return err
	}
	if err := fw.u32(uint32(len(re.tvgID))); err != nil {
		return err
	}

	return fw.str(re.tvgID)
}

// fieldSanitizer is built once; strings.NewReplacer costs ~7KB per construction.
var fieldSanitizer = strings.NewReplacer(
	"/", "_",
	"\\", "_",
	":", "_",
	"*", "_",
	"?", "_",
	"\"", "_",
	"<", "_",
	">", "_",
	"|", "_",
	" ", "",
)

const maxFieldRunes = 100

// sanitizeChars must list every fieldSanitizer key so the fast path only skips true no-ops.
const sanitizeChars = `/\:*?"<>| `

func sanitizeField(value string) string {
	sanitized := value
	if strings.ContainsAny(value, sanitizeChars) {
		sanitized = fieldSanitizer.Replace(value)
	}

	if len(sanitized) <= maxFieldRunes {
		return sanitized
	}

	runes := []rune(sanitized)
	if len(runes) > maxFieldRunes {
		sanitized = string(runes[:maxFieldRunes])
	}

	return sanitized
}
