package sourceproc

import (
	"bufio"
	"bytes"
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
	"unicode/utf8"
	"unsafe"

	"m3u-stream-merger/config"

	"github.com/cespare/xxhash"
)

// spillLayout pins resident fold memory at ~corpus/8 on any core count, capped to stay inside a 1024 FD limit.
func spillLayout() (nParts, conc int) {
	g := max(1, runtime.GOMAXPROCS(0))
	nParts = min(max(256, 8*g), 512)

	return nParts, max(1, min(g, nParts/8))
}

// partBufSize holds total phase-1 write buffering near 4MB regardless of partition count.
func partBufSize(nParts int) int {
	return max(16<<10, (4<<20)/nParts)
}

type spillPart struct {
	mu  sync.Mutex
	buf *bufio.Writer
	f   *os.File
}

// spillSorter is an external hash-partitioned sort: spill while parsing, fold and render partitions in parallel, k-way merge the runs.
type spillSorter struct {
	sortingKey string
	sortingDir string
	dir        string
	err        error
	conc       int
	parts      []*spillPart
	arenas     chan []byte
	scratch    sync.Pool
}

func newSpillSorter() *spillSorter {
	dir := config.GetSortDirPath()
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return &spillSorter{err: err}
	}

	nParts, conc := spillLayout()
	parts := make([]*spillPart, nParts)
	for i := range parts {
		parts[i] = &spillPart{}
	}

	sortingKey := os.Getenv("SORTING_KEY")
	if sortingKey == "" {
		sortingKey = "provider-order"
	}
	s := &spillSorter{
		sortingKey: sortingKey,
		sortingDir: strings.ToLower(os.Getenv("SORTING_DIRECTION")),
		dir:        dir,
		conc:       conc,
		parts:      parts,
		arenas:     make(chan []byte, conc),
	}
	s.scratch.New = func() any {
		b := make([]byte, 0, 1024)
		return &b
	}

	return s
}

func (s *spillSorter) partitionPath(i int) string {
	return filepath.Join(s.dir, fmt.Sprintf("p%04d.bin", i))
}

func (s *spillSorter) takeArena() []byte {
	select {
	case b := <-s.arenas:
		return b
	default:
		return nil
	}
}

func (s *spillSorter) putArena(b []byte) {
	select {
	case s.arenas <- b[:0]:
	default:
	}
}

func readFileInto(path string, b []byte) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return b, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return b, err
	}
	if info.Size() < 0 || info.Size() > int64(^uint(0)>>1) {
		_ = f.Close()
		return b, fmt.Errorf("spill partition is too large: %d", info.Size())
	}

	n := int(info.Size())
	if cap(b) < n {
		b = make([]byte, n)
	} else {
		b = b[:n]
	}
	if _, err := io.ReadFull(f, b); err != nil {
		_ = f.Close()
		return b, err
	}

	return b, f.Close()
}

func (s *spillSorter) Add(stream *StreamInfo) error {
	if s.err != nil {
		return s.err
	}

	bp := s.scratch.Get().(*[]byte)
	defer s.scratch.Put(bp)

	sanitized := appendSanitized((*bp)[:0], stream.Title)
	idx := int(xxhash.Sum64(sanitized) % uint64(len(s.parts)))
	*bp = appendStreamInfo(sanitized[:0], stream)
	rec := *bp
	p := s.parts[idx]
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.buf == nil {
		f, err := os.Create(s.partitionPath(idx))
		if err != nil {
			return err
		}
		p.f = f
		p.buf = bufio.NewWriterSize(f, partBufSize(len(s.parts)))
	}

	var lenBuf [4]byte
	binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(rec)))
	if _, err := p.buf.Write(lenBuf[:]); err != nil {
		return err
	}
	_, err := p.buf.Write(rec)

	return err
}

// renderedEntry is the pre-rendered per-entry payload; slices stay valid until the next render or merge step.
type renderedEntry struct {
	storeKey uint64
	m3u      []byte
	storeRec []byte
	tvgID    []byte
}

// renderBuf is per-worker render scratch, so a rendered entry costs no steady-state allocation.
type renderBuf struct {
	m3u  bytes.Buffer
	rec  []byte
	slug []byte
	tvg  []byte
}

func newRenderBuf() *renderBuf {
	return &renderBuf{slug: make([]byte, 0, slugBufSize)}
}

func (s *spillSorter) MergeRendered(render func(*StreamInfo, *renderBuf) renderedEntry, emit func(renderedEntry) error) error {
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
			partPaths = append(partPaths, s.partitionPath(i))
		}
		p.mu.Unlock()
	}

	var (
		mu       sync.Mutex
		firstErr error
		runPaths []string
		wg       sync.WaitGroup
	)
	sem := make(chan struct{}, s.conc)
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
func (s *spillSorter) buildRun(partPath string, render func(*StreamInfo, *renderBuf) renderedEntry) (string, error) {
	data, err := readFileInto(partPath, s.takeArena())
	defer s.putArena(data)
	if err != nil {
		return "", err
	}
	_ = os.Remove(partPath)

	records, totalURLs := 0, 0
	for rest := data; len(rest) > 0; records++ {
		if len(rest) < 4 {
			return "", errShortRecord
		}
		n := uint64(binary.LittleEndian.Uint32(rest))
		if n > uint64(len(rest)-4) {
			return "", errShortRecord
		}
		rec := rest[4 : 4+int(n)]
		urlCount, err := streamInfoURLCount(rec)
		if err != nil {
			return "", err
		}
		totalURLs += urlCount
		rest = rest[4+int(n):]
	}

	folded := make(map[string]*StreamInfo, records)
	infos := make([]StreamInfo, records)
	urls := make([]StreamURL, totalURLs)
	off, urlOff := 0, 0
	for i := range infos {
		n := int(binary.LittleEndian.Uint32(data[off:]))
		off += 4
		rec := data[off : off+n]
		off += n

		info := &infos[i]
		used, err := decodeStreamInfoInto(rec, info, urls[urlOff:])
		if err != nil {
			return "", err
		}
		urlOff += used

		key := sanitizeField(info.Title)
		if old, ok := folded[key]; ok {
			mergeStreamInfoAttributes(old, info)
		} else {
			folded[key] = info
		}
	}

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
	rb := newRenderBuf()
	for _, e := range entries {
		if err := fw.record(e, render(e.stream, rb)); err != nil {
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
	_ = os.RemoveAll(s.dir)
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
	case "provider-order", "source-order":
		e.key = providerOrderKey(s)
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

// providerOrderKey orders by provider position; fold keeps the min (source, line) of merged dupes.
func providerOrderKey(s *StreamInfo) string {
	src := s.SourceM3U
	if n, err := strconv.Atoi(strings.TrimPrefix(src, "M3U_")); err == nil {
		src = fmt.Sprintf("%08d", n)
	}

	return src + "\x00" + fmt.Sprintf("%011d", s.SourceIndex)
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

var errShortRecord = errors.New("sourceproc: truncated spill record")

func appendStr(dst []byte, s string) []byte {
	dst = binary.LittleEndian.AppendUint32(dst, uint32(len(s)))

	return append(dst, s...)
}

func appendBytes(dst, b []byte) []byte {
	dst = binary.LittleEndian.AppendUint32(dst, uint32(len(b)))

	return append(dst, b...)
}

func appendStreamInfo(dst []byte, s *StreamInfo) []byte {
	dst = appendStr(dst, s.Title)
	dst = appendStr(dst, s.TvgID)
	dst = appendStr(dst, s.TvgChNo)
	dst = appendStr(dst, s.TvgType)
	dst = appendStr(dst, s.LogoURL)
	dst = appendStr(dst, s.Group)
	dst = appendStr(dst, s.SourceM3U)
	dst = binary.LittleEndian.AppendUint32(dst, uint32(int32(s.SourceIndex)))
	dst = binary.LittleEndian.AppendUint32(dst, uint32(len(s.URLs)))
	for _, u := range s.URLs {
		dst = appendStr(dst, u.M3UIndex)
		dst = binary.LittleEndian.AppendUint32(dst, uint32(int32(u.LineNum)))
		dst = appendStr(dst, u.URL)
	}

	return dst
}

// binReader hands out string views into the record buffer, so decoding costs one allocation instead of one per field.
type binReader struct {
	b   []byte
	off int
}

func (r *binReader) u32() (uint32, error) {
	if r.off+4 > len(r.b) {
		return 0, errShortRecord
	}
	v := binary.LittleEndian.Uint32(r.b[r.off:])
	r.off += 4

	return v, nil
}

func (r *binReader) u64() (uint64, error) {
	if r.off+8 > len(r.b) {
		return 0, errShortRecord
	}
	v := binary.LittleEndian.Uint64(r.b[r.off:])
	r.off += 8

	return v, nil
}

func (r *binReader) raw() ([]byte, error) {
	n, err := r.u32()
	if err != nil {
		return nil, err
	}
	if uint64(n) > uint64(len(r.b)-r.off) {
		return nil, errShortRecord
	}
	b := r.b[r.off : r.off+int(n)]
	r.off += int(n)

	return b, nil
}

func (r *binReader) str() (string, error) {
	b, err := r.raw()
	if err != nil || len(b) == 0 {
		return "", err
	}

	return unsafe.String(&b[0], len(b)), nil
}

func streamInfoURLCount(rec []byte) (int, error) {
	r := binReader{b: rec}
	for range 7 {
		if _, err := r.raw(); err != nil {
			return 0, err
		}
	}
	if _, err := r.u32(); err != nil {
		return 0, err
	}
	n, err := r.u32()
	if err != nil {
		return 0, err
	}
	if uint64(n) > uint64(len(rec)-r.off)/12 {
		return 0, errShortRecord
	}

	return int(n), nil
}

// decodeStreamInfo fills s with views into rec, so rec must stay alive as long as s does.
func decodeStreamInfo(rec []byte, s *StreamInfo) error {
	_, err := decodeStreamInfoInto(rec, s, nil)

	return err
}

func decodeStreamInfoInto(rec []byte, s *StreamInfo, urlBuf []StreamURL) (int, error) {
	r := binReader{b: rec}
	fields := []*string{&s.Title, &s.TvgID, &s.TvgChNo, &s.TvgType, &s.LogoURL, &s.Group, &s.SourceM3U}
	for _, f := range fields {
		v, err := r.str()
		if err != nil {
			return 0, err
		}
		*f = v
	}

	srcIdx, err := r.u32()
	if err != nil {
		return 0, err
	}
	s.SourceIndex = int(int32(srcIdx))

	n, err := r.u32()
	if err != nil {
		return 0, err
	}
	if uint64(n) > uint64(len(rec)-r.off)/12 {
		return 0, errShortRecord
	}
	if n == 0 {
		s.URLs = nil
		return 0, nil
	}

	if len(urlBuf) < int(n) {
		urlBuf = make([]StreamURL, n)
	}
	urls := urlBuf[:n:n]
	for i := range urls {
		if urls[i].M3UIndex, err = r.str(); err != nil {
			return 0, err
		}
		lineNum, err := r.u32()
		if err != nil {
			return 0, err
		}
		urls[i].LineNum = int(int32(lineNum))
		if urls[i].URL, err = r.str(); err != nil {
			return 0, err
		}
	}
	s.URLs = urls

	return len(urls), nil
}

type runEntry struct {
	se sortEntry
	re renderedEntry
}

type runReader struct {
	f   *os.File
	br  *bufio.Reader
	buf []byte
	cur runEntry
	ok  bool
}

func openRun(path string) (*runReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	return &runReader{f: f, br: bufio.NewReaderSize(f, 16<<10)}, nil
}

// next reads one whole record into the reusable buffer; cur then only holds views, valid until the following next.
func (r *runReader) next() error {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r.br, lenBuf[:]); err != nil {
		if errors.Is(err, io.EOF) {
			r.ok = false
			return nil
		}
		return err
	}

	n := int(binary.LittleEndian.Uint32(lenBuf[:]))
	if cap(r.buf) < n {
		r.buf = make([]byte, n)
	}
	r.buf = r.buf[:n]
	if _, err := io.ReadFull(r.br, r.buf); err != nil {
		return err
	}

	br := binReader{b: r.buf}
	var (
		e   runEntry
		err error
	)
	if e.se.key, err = br.str(); err != nil {
		return err
	}
	num, err := br.u32()
	if err != nil {
		return err
	}
	numOK, err := br.raw()
	if err != nil {
		return err
	}
	e.se.num, e.se.numOK = int(int32(num)), len(numOK) == 1 && numOK[0] == 1
	if e.se.title, err = br.str(); err != nil {
		return err
	}
	if e.re.storeKey, err = br.u64(); err != nil {
		return err
	}
	if e.re.m3u, err = br.raw(); err != nil {
		return err
	}
	if e.re.storeRec, err = br.raw(); err != nil {
		return err
	}
	if e.re.tvgID, err = br.raw(); err != nil {
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
	buf []byte
}

// record frames one rendered entry: sort key first so the merge heap never decodes payloads, then the rendered bytes.
func (fw *frameWriter) record(e sortEntry, re renderedEntry) error {
	numOK := []byte{0}
	if e.numOK {
		numOK[0] = 1
	}

	b := appendStr(fw.buf[:0], e.key)
	b = binary.LittleEndian.AppendUint32(b, uint32(int32(e.num)))
	b = appendBytes(b, numOK)
	b = appendStr(b, e.title)
	b = binary.LittleEndian.AppendUint64(b, re.storeKey)
	b = appendBytes(b, re.m3u)
	b = appendBytes(b, re.storeRec)
	b = appendBytes(b, re.tvgID)
	fw.buf = b

	var lenBuf [4]byte
	binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(b)))
	if _, err := fw.w.Write(lenBuf[:]); err != nil {
		return err
	}
	_, err := fw.w.Write(b)

	return err
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

func appendSanitized(dst []byte, value string) []byte {
	start := len(dst)
	for len(value) > 0 {
		r, size := utf8.DecodeRuneInString(value)
		raw := value[:size]
		value = value[size:]
		if r == ' ' {
			continue
		}
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			dst = append(dst, '_')
		default:
			dst = append(dst, raw...)
		}
	}

	out := dst[start:]
	if len(out) <= maxFieldRunes {
		return dst
	}

	value = unsafe.String(unsafe.SliceData(out), len(out))
	if utf8.ValidString(value) {
		n := 0
		for i := range value {
			if n == maxFieldRunes {
				return dst[:start+i]
			}
			n++
		}
		return dst
	}

	runes := []rune(value)
	if len(runes) <= maxFieldRunes {
		return dst
	}
	dst = dst[:start]
	for _, r := range runes[:maxFieldRunes] {
		dst = utf8.AppendRune(dst, r)
	}

	return dst
}

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
