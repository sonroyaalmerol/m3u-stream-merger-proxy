package sourceproc

import (
	"bufio"
	"cmp"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"m3u-stream-merger/config"

	"github.com/cespare/xxhash"
	"golang.org/x/sys/unix"
)

const (
	catalogMagic          = "M3UCAT04"
	catalogHeaderLen      = 128
	catalogRecordFixedLen = 64
	lookupRecordLen       = 12
	categoryRecordLen     = 16
	seriesRecordLen       = 32
	episodeRecordLen      = 16
	catalogKindLive       = 1
	catalogKindMovie      = 2
	catalogKindSeries     = 3
	CatalogLive           = "live"
	CatalogMovie          = "movie"
	CatalogSeries         = "series"
)

type catalogHeader struct {
	dataSize       uint64
	recordCount    uint32
	categoryCount  uint32
	memberCount    uint32
	seriesCount    uint32
	episodeCount   uint32
	offsetsOff     uint64
	slugOff        uint64
	streamIDOff    uint64
	categoriesOff  uint64
	membersOff     uint64
	seriesOff      uint64
	seriesOrderOff uint64
	episodesOff    uint64
}

type lookupEntry struct {
	key      uint64
	recordID uint32
}

type categoryBuild struct {
	key      uint64
	recordID uint32
	kind     byte
	name     string
	sortName string
}

type seriesBuild struct {
	key          uint64
	recordID     uint32
	categoryID   uint64
	episodeStart uint32
	episodeCount uint32
	position     uint32
	name         string
	sortName     string
}

type episodeEntry struct {
	seriesID uint64
	recordID uint32
	season   uint16
	episode  uint16
}

type CatalogEntry struct {
	StreamID   uint64
	CategoryID uint64
	SeriesID   uint64
	Title      string
	Show       string
	TvgID      string
	Group      string
	Logo       string
	Type       string
	Slug       string
	BasePath   string
	Ext        string
	Season     int
	Episode    int
}

type CatalogCategory struct {
	ID   uint64
	Name string
	Type string
}

type CatalogSeriesEntry struct {
	SeriesID   uint64
	CategoryID uint64
	Name       string
	Group      string
	Cover      string
	Episodes   map[int][]CatalogEntry
}

type StreamStore struct {
	mu        sync.RWMutex
	gen       uint64
	dataFile  *os.File
	indexFile *os.File
	data      []byte
	index     []byte
	header    catalogHeader
	loaded    bool
}

var defaultStore = &StreamStore{}

func storeDir() string    { return config.GetStreamStoreDirPath() }
func currentPath() string { return filepath.Join(storeDir(), "current") }
func dataPath(gen uint64) string {
	return filepath.Join(storeDir(), fmt.Sprintf("g%d.cat", gen))
}
func indexPath(gen uint64) string {
	return filepath.Join(storeDir(), fmt.Sprintf("g%d.cix", gen))
}

func GetStreamStore() *StreamStore { return defaultStore }

func slugDigest(slug string) ([28]byte, error) {
	var sum [28]byte
	n, err := base64.RawURLEncoding.Decode(sum[:], []byte(slug))
	if err != nil || n != len(sum) {
		return sum, fmt.Errorf("invalid slug: %s", slug)
	}
	return sum, nil
}

func appendCatalogRecord(dst []byte, sum [28]byte, s *StreamInfo) []byte {
	kind := catalogKind(s.TvgType)
	show, season, episode := parseEpisodeTitle(s.Title)
	if kind == catalogKindSeries && show == "" {
		kind = catalogKindLive
	}

	categoryID := uint64(0)
	if s.Group != "" {
		categoryID = categoryIDFor(kind, s.Group)
	}

	seriesID := uint64(0)
	if kind == catalogKindSeries {
		seriesID = SeriesIDFor(show)
	}

	ext := streamExtension(s)
	if len(ext) > math.MaxUint8 {
		ext = ""
	}

	dst = append(dst, sum[:]...)
	dst = binary.LittleEndian.AppendUint64(dst, StreamIDFor(s.Title))
	dst = binary.LittleEndian.AppendUint64(dst, categoryID)
	dst = binary.LittleEndian.AppendUint64(dst, seriesID)
	dst = binary.LittleEndian.AppendUint32(dst, uint32(len(show)))
	dst = binary.LittleEndian.AppendUint16(dst, uint16(season))
	dst = binary.LittleEndian.AppendUint16(dst, uint16(episode))
	dst = append(dst, kind, byte(len(ext)), 0, 0)
	dst = append(dst, ext...)

	return appendStreamInfo(dst, s)
}

func catalogKind(tvgType string) byte {
	switch {
	case strings.EqualFold(tvgType, CatalogMovie):
		return catalogKindMovie
	case strings.EqualFold(tvgType, CatalogSeries):
		return catalogKindSeries
	default:
		return catalogKindLive
	}
}

// ponytail: 63-bit mask keeps every emitted ID inside a Java Long, unlike raw xxhash
func StreamIDFor(title string) uint64 {
	return xxhash.Sum64String(title) & 0x7fffffffffffffff
}

func SeriesIDFor(show string) uint64 {
	h := xxhash.New()
	_, _ = h.Write([]byte("series|"))
	_, _ = h.Write([]byte(show))
	return h.Sum64() & 0x7fffffffffffffff
}

func catalogKindName(kind byte) string {
	switch kind {
	case catalogKindMovie:
		return CatalogMovie
	case catalogKindSeries:
		return CatalogSeries
	default:
		return CatalogLive
	}
}

func categoryIDFor(kind byte, group string) uint64 {
	return (xxhash.Sum64String(group) ^ (uint64(kind) * 0x9e3779b185ebca87)) & 0x7fffffff
}

func parseEpisodeTitle(title string) (string, int, int) {
	space := strings.LastIndexByte(title, ' ')
	if space <= 0 || space+4 >= len(title) {
		return "", 0, 0
	}
	suffix := title[space+1:]
	if suffix[0] != 's' && suffix[0] != 'S' {
		return "", 0, 0
	}

	i := 1
	season, digits := parseDigits(suffix[i:], 4)
	if digits == 0 {
		return "", 0, 0
	}
	i += digits
	if i >= len(suffix) || (suffix[i] != 'e' && suffix[i] != 'E') {
		return "", 0, 0
	}
	i++
	episode, digits := parseDigits(suffix[i:], 4)
	if digits == 0 || i+digits != len(suffix) {
		return "", 0, 0
	}

	return strings.TrimSpace(title[:space]), season, episode
}

func parseDigits(s string, limit int) (int, int) {
	v := 0
	i := 0
	for i < len(s) && i < limit && s[i] >= '0' && s[i] <= '9' {
		v = v*10 + int(s[i]-'0')
		i++
	}
	return v, i
}

func streamExtension(s *StreamInfo) string {
	if len(s.URLs) == 0 {
		return ""
	}
	u := s.URLs[0].URL
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	slash := strings.LastIndexByte(u, '/')
	dot := strings.LastIndexByte(u, '.')
	if dot <= slash || dot == len(u)-1 {
		return ""
	}
	ext := u[dot:]
	if strings.EqualFold(ext, ".m3u") || strings.EqualFold(ext, ".m3u8") {
		return ""
	}
	return ext
}

func catalogRecordPayload(rec []byte) ([]byte, error) {
	if len(rec) < catalogRecordFixedLen {
		return nil, fmt.Errorf("catalog record too short")
	}
	extLen := int(rec[61])
	if catalogRecordFixedLen+extLen > len(rec) {
		return nil, fmt.Errorf("catalog record extension truncated")
	}
	return rec[catalogRecordFixedLen+extLen:], nil
}

func catalogRecordMeta(rec []byte) (StreamInfo, error) {
	payload, err := catalogRecordPayload(rec)
	if err != nil {
		return StreamInfo{}, err
	}
	r := binReader{b: payload}
	var info StreamInfo
	fields := []*string{&info.Title, &info.TvgID, &info.TvgChNo, &info.TvgType, &info.LogoURL, &info.Group}
	for _, field := range fields {
		value, err := r.str()
		if err != nil {
			return StreamInfo{}, err
		}
		*field = value
	}
	return info, nil
}

func decodeCatalogEntry(rec []byte) (CatalogEntry, error) {
	info, err := catalogRecordMeta(rec)
	if err != nil {
		return CatalogEntry{}, err
	}
	showLen := binary.LittleEndian.Uint32(rec[52:])
	if uint64(showLen) > uint64(len(info.Title)) {
		return CatalogEntry{}, fmt.Errorf("catalog record show name truncated")
	}

	kind := rec[60]
	extLen := int(rec[61])
	entry := CatalogEntry{
		StreamID:   binary.LittleEndian.Uint64(rec[28:]),
		CategoryID: binary.LittleEndian.Uint64(rec[36:]),
		SeriesID:   binary.LittleEndian.Uint64(rec[44:]),
		Title:      info.Title,
		Show:       info.Title[:showLen],
		TvgID:      info.TvgID,
		Group:      info.Group,
		Logo:       info.LogoURL,
		Type:       catalogKindName(kind),
		Ext:        string(rec[catalogRecordFixedLen : catalogRecordFixedLen+extLen]),
		Season:     int(binary.LittleEndian.Uint16(rec[56:])),
		Episode:    int(binary.LittleEndian.Uint16(rec[58:])),
	}
	return entry, nil
}

func cloneCatalogEntry(entry CatalogEntry) CatalogEntry {
	entry.Title = strings.Clone(entry.Title)
	entry.Show = strings.Clone(entry.Show)
	entry.TvgID = strings.Clone(entry.TvgID)
	entry.Group = strings.Clone(entry.Group)
	entry.Logo = strings.Clone(entry.Logo)
	entry.Ext = strings.Clone(entry.Ext)
	return entry
}

func (s *StreamStore) Get(slug string) (*StreamInfo, error) {
	sum, err := slugDigest(slug)
	if err != nil {
		return nil, err
	}
	if err := s.ensureLoaded(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	key := binary.BigEndian.Uint64(sum[:8])
	start, end := s.lookupRange(s.header.slugOff, key)
	for i := start; i < end; i++ {
		recordID := s.lookupRecordID(s.header.slugOff, i)
		rec, err := s.record(recordID)
		if err != nil || len(rec) < len(sum) || !slices.Equal(rec[:len(sum)], sum[:]) {
			continue
		}
		payload, err := catalogRecordPayload(rec)
		if err != nil {
			continue
		}
		owned := append([]byte(nil), payload...)
		var info StreamInfo
		if err := decodeStreamInfo(owned, &info); err != nil {
			continue
		}
		info.backing = owned
		return &info, nil
	}
	return nil, fmt.Errorf("slug not found: %s", slug)
}

func (s *StreamStore) ensureLoaded() error {
	s.mu.RLock()
	loaded := s.loaded
	s.mu.RUnlock()
	if loaded {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded {
		return nil
	}
	return s.loadLocked()
}

func (s *StreamStore) loadLocked() error {
	raw, err := os.ReadFile(currentPath())
	if err != nil {
		return fmt.Errorf("stream catalog unavailable: %w", err)
	}
	gen, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return fmt.Errorf("stream catalog pointer corrupt: %w", err)
	}

	dataFile, dataMap, err := mmapFile(dataPath(gen))
	if err != nil {
		return fmt.Errorf("stream catalog unavailable: %w", err)
	}
	indexFile, indexMap, err := mmapFile(indexPath(gen))
	if err != nil {
		_ = unix.Munmap(dataMap)
		_ = dataFile.Close()
		return fmt.Errorf("stream catalog unavailable: %w", err)
	}
	header, err := parseCatalogHeader(indexMap, uint64(len(dataMap)))
	if err != nil {
		_ = unix.Munmap(indexMap)
		_ = unix.Munmap(dataMap)
		_ = indexFile.Close()
		_ = dataFile.Close()
		return err
	}

	s.closeLocked()
	s.gen = gen
	s.dataFile = dataFile
	s.indexFile = indexFile
	s.data = dataMap
	s.index = indexMap
	s.header = header
	s.loaded = true
	return nil
}

func mmapFile(path string) (*os.File, []byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	stat, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if stat.Size() < 0 || uint64(stat.Size()) > uint64(^uint(0)>>1) {
		_ = file.Close()
		return nil, nil, fmt.Errorf("invalid mapped file size: %d", stat.Size())
	}
	if stat.Size() == 0 {
		_ = file.Close()
		return nil, nil, nil
	}
	mapped, err := unix.Mmap(int(file.Fd()), 0, int(stat.Size()), unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	return file, mapped, nil
}

func (s *StreamStore) closeLocked() {
	if s.index != nil {
		_ = unix.Munmap(s.index)
	}
	if s.data != nil {
		_ = unix.Munmap(s.data)
	}
	if s.indexFile != nil {
		_ = s.indexFile.Close()
	}
	if s.dataFile != nil {
		_ = s.dataFile.Close()
	}
	s.dataFile = nil
	s.indexFile = nil
	s.data = nil
	s.index = nil
	s.loaded = false
}

func (s *StreamStore) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeLocked()
}

func parseCatalogHeader(index []byte, dataSize uint64) (catalogHeader, error) {
	if len(index) < catalogHeaderLen || string(index[:8]) != catalogMagic {
		return catalogHeader{}, fmt.Errorf("stream catalog index corrupt")
	}
	if binary.LittleEndian.Uint32(index[8:]) != catalogHeaderLen {
		return catalogHeader{}, fmt.Errorf("stream catalog header unsupported")
	}
	h := catalogHeader{
		dataSize:       binary.LittleEndian.Uint64(index[16:]),
		recordCount:    binary.LittleEndian.Uint32(index[24:]),
		categoryCount:  binary.LittleEndian.Uint32(index[28:]),
		memberCount:    binary.LittleEndian.Uint32(index[32:]),
		seriesCount:    binary.LittleEndian.Uint32(index[36:]),
		episodeCount:   binary.LittleEndian.Uint32(index[40:]),
		offsetsOff:     binary.LittleEndian.Uint64(index[48:]),
		slugOff:        binary.LittleEndian.Uint64(index[56:]),
		streamIDOff:    binary.LittleEndian.Uint64(index[64:]),
		categoriesOff:  binary.LittleEndian.Uint64(index[72:]),
		membersOff:     binary.LittleEndian.Uint64(index[80:]),
		seriesOff:      binary.LittleEndian.Uint64(index[88:]),
		seriesOrderOff: binary.LittleEndian.Uint64(index[96:]),
		episodesOff:    binary.LittleEndian.Uint64(index[104:]),
	}
	if h.dataSize != dataSize {
		return catalogHeader{}, fmt.Errorf("stream catalog data size mismatch")
	}
	sections := []struct {
		off   uint64
		count uint64
		width uint64
	}{
		{h.offsetsOff, uint64(h.recordCount), 8},
		{h.slugOff, uint64(h.recordCount), lookupRecordLen},
		{h.streamIDOff, uint64(h.recordCount), lookupRecordLen},
		{h.categoriesOff, uint64(h.categoryCount), categoryRecordLen},
		{h.membersOff, uint64(h.memberCount), lookupRecordLen},
		{h.seriesOff, uint64(h.seriesCount), seriesRecordLen},
		{h.seriesOrderOff, uint64(h.seriesCount), 4},
		{h.episodesOff, uint64(h.episodeCount), episodeRecordLen},
	}
	for _, section := range sections {
		if section.off < catalogHeaderLen || section.off > uint64(len(index)) || section.count > (uint64(len(index))-min(section.off, uint64(len(index))))/section.width {
			return catalogHeader{}, fmt.Errorf("stream catalog section corrupt")
		}
	}
	return h, nil
}

func (s *StreamStore) record(recordID uint32) ([]byte, error) {
	if recordID >= s.header.recordCount {
		return nil, fmt.Errorf("catalog record id out of range")
	}
	pos := s.header.offsetsOff + uint64(recordID)*8
	off := binary.LittleEndian.Uint64(s.index[pos:])
	if off < 4 || off > uint64(len(s.data)) {
		return nil, fmt.Errorf("catalog record offset corrupt")
	}
	size := uint64(binary.LittleEndian.Uint32(s.data[off-4:]))
	if size > uint64(len(s.data))-off {
		return nil, fmt.Errorf("catalog record truncated")
	}
	return s.data[off : off+size], nil
}

func (s *StreamStore) lookupRange(off uint64, key uint64) (int, int) {
	count := int(s.header.recordCount)
	start := sortSearch(count, func(i int) bool {
		return binary.LittleEndian.Uint64(s.index[off+uint64(i)*lookupRecordLen:]) >= key
	})
	end := start
	for end < count && binary.LittleEndian.Uint64(s.index[off+uint64(end)*lookupRecordLen:]) == key {
		end++
	}
	return start, end
}

func (s *StreamStore) lookupRecordID(off uint64, index int) uint32 {
	pos := off + uint64(index)*lookupRecordLen + 8
	return binary.LittleEndian.Uint32(s.index[pos:])
}

type StreamStoreWriter struct {
	gen             uint64
	file            *os.File
	buf             *bufio.Writer
	off             uint64
	offsets         []uint64
	slugIndex       []lookupEntry
	streamIDIndex   []lookupEntry
	categoryMembers []lookupEntry
	categories      map[uint64]*categoryBuild
	series          map[uint64]*seriesBuild
	episodes        []episodeEntry
	scratch         []byte
}

func NewStreamStoreWriter() (*StreamStoreWriter, error) {
	if err := os.MkdirAll(storeDir(), os.ModePerm); err != nil {
		return nil, err
	}
	gen := uint64(1)
	if raw, err := os.ReadFile(currentPath()); err == nil {
		if cur, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64); err == nil {
			gen = cur + 1
		}
	}
	file, err := os.Create(dataPath(gen))
	if err != nil {
		return nil, err
	}
	return &StreamStoreWriter{
		gen:        gen,
		file:       file,
		buf:        bufio.NewWriterSize(file, 1<<20),
		categories: make(map[uint64]*categoryBuild),
		series:     make(map[uint64]*seriesBuild),
	}, nil
}

func (w *StreamStoreWriter) reserve(n int) {
	if n <= 0 {
		return
	}
	w.offsets = make([]uint64, 0, n)
	w.slugIndex = make([]lookupEntry, 0, n)
	w.streamIDIndex = make([]lookupEntry, 0, n)
	w.categoryMembers = make([]lookupEntry, 0, n)
}

func (w *StreamStoreWriter) Add(key uint64, stream *StreamInfo) error {
	_, sum := slugParts(stream.Title)
	w.scratch = appendCatalogRecord(w.scratch[:0], sum, stream)
	return w.AddRaw(key, w.scratch)
}

func (w *StreamStoreWriter) AddRaw(key uint64, record []byte) error {
	if len(record) < catalogRecordFixedLen || uint64(len(record)) > math.MaxUint32 || len(w.offsets) == math.MaxUint32 {
		return fmt.Errorf("invalid catalog record")
	}
	if _, err := catalogRecordPayload(record); err != nil {
		return err
	}
	meta, err := catalogRecordMeta(record)
	if err != nil {
		return err
	}

	recordID := uint32(len(w.offsets))
	var frame [4]byte
	binary.LittleEndian.PutUint32(frame[:], uint32(len(record)))
	if _, err := w.buf.Write(frame[:]); err != nil {
		return err
	}
	if _, err := w.buf.Write(record); err != nil {
		return err
	}
	w.off += 4
	w.offsets = append(w.offsets, w.off)
	w.off += uint64(len(record))

	streamID := binary.LittleEndian.Uint64(record[28:])
	categoryID := binary.LittleEndian.Uint64(record[36:])
	seriesID := binary.LittleEndian.Uint64(record[44:])
	kind := record[60]
	w.slugIndex = append(w.slugIndex, lookupEntry{key: key, recordID: recordID})
	w.streamIDIndex = append(w.streamIDIndex, lookupEntry{key: streamID, recordID: recordID})

	if categoryID != 0 {
		w.categoryMembers = append(w.categoryMembers, lookupEntry{key: categoryID, recordID: recordID})
		if _, ok := w.categories[categoryID]; !ok {
			name := strings.Clone(meta.Group)
			w.categories[categoryID] = &categoryBuild{key: categoryID, recordID: recordID, kind: kind, name: name, sortName: strings.ToLower(name)}
		}
	}
	if seriesID != 0 {
		showLen := binary.LittleEndian.Uint32(record[52:])
		if uint64(showLen) > uint64(len(meta.Title)) {
			return fmt.Errorf("invalid series title")
		}
		if _, ok := w.series[seriesID]; !ok {
			name := strings.Clone(meta.Title[:showLen])
			w.series[seriesID] = &seriesBuild{key: seriesID, recordID: recordID, categoryID: categoryID, name: name, sortName: strings.ToLower(name)}
		}
		w.episodes = append(w.episodes, episodeEntry{
			seriesID: seriesID,
			recordID: recordID,
			season:   binary.LittleEndian.Uint16(record[56:]),
			episode:  binary.LittleEndian.Uint16(record[58:]),
		})
	}
	return nil
}

func (w *StreamStoreWriter) Commit() error {
	if err := w.buf.Flush(); err != nil {
		_ = w.file.Close()
		return err
	}
	if err := w.file.Sync(); err != nil {
		_ = w.file.Close()
		return err
	}
	if err := w.file.Close(); err != nil {
		return err
	}
	if err := w.writeIndex(); err != nil {
		return err
	}

	ptr, err := os.Create(currentPath() + ".new")
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(ptr, "%d", w.gen); err != nil {
		_ = ptr.Close()
		return err
	}
	if err := ptr.Sync(); err != nil {
		_ = ptr.Close()
		return err
	}
	if err := ptr.Close(); err != nil {
		return err
	}
	if err := os.Rename(currentPath()+".new", currentPath()); err != nil {
		return err
	}
	if err := syncDir(storeDir()); err != nil {
		return err
	}

	defaultStore.mu.Lock()
	defer defaultStore.mu.Unlock()
	if err := defaultStore.loadLocked(); err != nil {
		return err
	}
	if w.gen > 1 {
		_ = os.Remove(dataPath(w.gen - 1))
		_ = os.Remove(indexPath(w.gen - 1))
	}
	return nil
}

func (w *StreamStoreWriter) writeIndex() error {
	slices.SortFunc(w.slugIndex, compareLookup)
	slices.SortFunc(w.streamIDIndex, compareLookup)
	slices.SortFunc(w.categoryMembers, compareLookup)
	slices.SortFunc(w.episodes, func(a, b episodeEntry) int {
		if c := cmp.Compare(a.seriesID, b.seriesID); c != 0 {
			return c
		}
		if c := cmp.Compare(a.season, b.season); c != 0 {
			return c
		}
		if c := cmp.Compare(a.episode, b.episode); c != 0 {
			return c
		}
		return cmp.Compare(a.recordID, b.recordID)
	})

	categories := make([]*categoryBuild, 0, len(w.categories))
	for _, category := range w.categories {
		categories = append(categories, category)
	}
	slices.SortFunc(categories, func(a, b *categoryBuild) int {
		if c := cmp.Compare(a.kind, b.kind); c != 0 {
			return c
		}
		if c := strings.Compare(a.sortName, b.sortName); c != 0 {
			return c
		}
		return strings.Compare(a.name, b.name)
	})

	series := make([]*seriesBuild, 0, len(w.series))
	for _, item := range w.series {
		series = append(series, item)
	}
	slices.SortFunc(series, func(a, b *seriesBuild) int { return cmp.Compare(a.key, b.key) })
	for i, item := range series {
		item.position = uint32(i)
	}
	for i := 0; i < len(w.episodes); {
		start := i
		id := w.episodes[i].seriesID
		for i < len(w.episodes) && w.episodes[i].seriesID == id {
			i++
		}
		if item := w.series[id]; item != nil {
			item.episodeStart = uint32(start)
			item.episodeCount = uint32(i - start)
		}
	}
	seriesOrder := slices.Clone(series)
	slices.SortFunc(seriesOrder, func(a, b *seriesBuild) int {
		if c := strings.Compare(a.sortName, b.sortName); c != 0 {
			return c
		}
		return strings.Compare(a.name, b.name)
	})

	file, err := os.Create(indexPath(w.gen))
	if err != nil {
		return err
	}
	if _, err := file.Write(make([]byte, catalogHeaderLen)); err != nil {
		_ = file.Close()
		return err
	}
	out := bufio.NewWriterSize(file, 1<<20)
	off := uint64(catalogHeaderLen)
	header := catalogHeader{
		dataSize:      w.off,
		recordCount:   uint32(len(w.offsets)),
		categoryCount: uint32(len(categories)),
		memberCount:   uint32(len(w.categoryMembers)),
		seriesCount:   uint32(len(series)),
		episodeCount:  uint32(len(w.episodes)),
	}

	header.offsetsOff = off
	for _, value := range w.offsets {
		if err := writeU64(out, value); err != nil {
			return closeIndexOnError(file, err)
		}
		off += 8
	}
	header.slugOff = off
	if err := writeLookups(out, w.slugIndex); err != nil {
		return closeIndexOnError(file, err)
	}
	off += uint64(len(w.slugIndex) * lookupRecordLen)
	header.streamIDOff = off
	if err := writeLookups(out, w.streamIDIndex); err != nil {
		return closeIndexOnError(file, err)
	}
	off += uint64(len(w.streamIDIndex) * lookupRecordLen)
	header.categoriesOff = off
	for _, item := range categories {
		var rec [categoryRecordLen]byte
		binary.LittleEndian.PutUint64(rec[:], item.key)
		binary.LittleEndian.PutUint32(rec[8:], item.recordID)
		rec[12] = item.kind
		if _, err := out.Write(rec[:]); err != nil {
			return closeIndexOnError(file, err)
		}
		off += categoryRecordLen
	}
	header.membersOff = off
	if err := writeLookups(out, w.categoryMembers); err != nil {
		return closeIndexOnError(file, err)
	}
	off += uint64(len(w.categoryMembers) * lookupRecordLen)
	header.seriesOff = off
	for _, item := range series {
		var rec [seriesRecordLen]byte
		binary.LittleEndian.PutUint64(rec[:], item.key)
		binary.LittleEndian.PutUint32(rec[8:], item.recordID)
		binary.LittleEndian.PutUint64(rec[12:], item.categoryID)
		binary.LittleEndian.PutUint32(rec[20:], item.episodeStart)
		binary.LittleEndian.PutUint32(rec[24:], item.episodeCount)
		if _, err := out.Write(rec[:]); err != nil {
			return closeIndexOnError(file, err)
		}
		off += seriesRecordLen
	}
	header.seriesOrderOff = off
	for _, item := range seriesOrder {
		var rec [4]byte
		binary.LittleEndian.PutUint32(rec[:], item.position)
		if _, err := out.Write(rec[:]); err != nil {
			return closeIndexOnError(file, err)
		}
		off += 4
	}
	header.episodesOff = off
	for _, item := range w.episodes {
		var rec [episodeRecordLen]byte
		binary.LittleEndian.PutUint64(rec[:], item.seriesID)
		binary.LittleEndian.PutUint32(rec[8:], item.recordID)
		binary.LittleEndian.PutUint16(rec[12:], item.season)
		binary.LittleEndian.PutUint16(rec[14:], item.episode)
		if _, err := out.Write(rec[:]); err != nil {
			return closeIndexOnError(file, err)
		}
	}
	if err := out.Flush(); err != nil {
		return closeIndexOnError(file, err)
	}
	if _, err := file.WriteAt(encodeCatalogHeader(header), 0); err != nil {
		return closeIndexOnError(file, err)
	}
	if err := file.Sync(); err != nil {
		return closeIndexOnError(file, err)
	}
	return file.Close()
}

func compareLookup(a, b lookupEntry) int {
	if c := cmp.Compare(a.key, b.key); c != 0 {
		return c
	}
	return cmp.Compare(a.recordID, b.recordID)
}

func writeLookups(w *bufio.Writer, entries []lookupEntry) error {
	var rec [lookupRecordLen]byte
	for _, item := range entries {
		binary.LittleEndian.PutUint64(rec[:], item.key)
		binary.LittleEndian.PutUint32(rec[8:], item.recordID)
		if _, err := w.Write(rec[:]); err != nil {
			return err
		}
	}
	return nil
}

func writeU64(w *bufio.Writer, value uint64) error {
	var rec [8]byte
	binary.LittleEndian.PutUint64(rec[:], value)
	_, err := w.Write(rec[:])
	return err
}

func encodeCatalogHeader(h catalogHeader) []byte {
	out := make([]byte, catalogHeaderLen)
	copy(out, catalogMagic)
	binary.LittleEndian.PutUint32(out[8:], catalogHeaderLen)
	binary.LittleEndian.PutUint64(out[16:], h.dataSize)
	binary.LittleEndian.PutUint32(out[24:], h.recordCount)
	binary.LittleEndian.PutUint32(out[28:], h.categoryCount)
	binary.LittleEndian.PutUint32(out[32:], h.memberCount)
	binary.LittleEndian.PutUint32(out[36:], h.seriesCount)
	binary.LittleEndian.PutUint32(out[40:], h.episodeCount)
	binary.LittleEndian.PutUint64(out[48:], h.offsetsOff)
	binary.LittleEndian.PutUint64(out[56:], h.slugOff)
	binary.LittleEndian.PutUint64(out[64:], h.streamIDOff)
	binary.LittleEndian.PutUint64(out[72:], h.categoriesOff)
	binary.LittleEndian.PutUint64(out[80:], h.membersOff)
	binary.LittleEndian.PutUint64(out[88:], h.seriesOff)
	binary.LittleEndian.PutUint64(out[96:], h.seriesOrderOff)
	binary.LittleEndian.PutUint64(out[104:], h.episodesOff)
	return out
}

func closeIndexOnError(file *os.File, err error) error {
	_ = file.Close()
	return err
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

func (w *StreamStoreWriter) Discard() {
	_ = w.file.Close()
	_ = os.Remove(dataPath(w.gen))
	_ = os.Remove(indexPath(w.gen))
	_ = os.Remove(currentPath() + ".new")
}

func (s *StreamStore) RangeEntries(kind string, categoryID uint64, yield func(int, CatalogEntry) bool) error {
	if err := s.ensureLoaded(); err != nil {
		return err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	wantKind := catalogKind(kind)
	position := 0
	visit := func(recordID uint32) bool {
		rec, err := s.record(recordID)
		if err != nil || rec[60] != wantKind {
			return true
		}
		entry, err := decodeCatalogEntry(rec)
		if err != nil {
			return true
		}
		position++
		return yield(position, entry)
	}

	if categoryID == 0 {
		for recordID := range s.header.recordCount {
			if !visit(recordID) {
				break
			}
		}
		return nil
	}

	start, end := s.memberRange(categoryID)
	for i := start; i < end; i++ {
		if !visit(s.lookupRecordID(s.header.membersOff, i)) {
			break
		}
	}
	return nil
}

func (s *StreamStore) memberRange(key uint64) (int, int) {
	count := int(s.header.memberCount)
	start := sortSearch(count, func(i int) bool {
		return binary.LittleEndian.Uint64(s.index[s.header.membersOff+uint64(i)*lookupRecordLen:]) >= key
	})
	end := start
	for end < count && binary.LittleEndian.Uint64(s.index[s.header.membersOff+uint64(end)*lookupRecordLen:]) == key {
		end++
	}
	return start, end
}

func sortSearch(n int, f func(int) bool) int {
	i, j := 0, n
	for i < j {
		h := int(uint(i+j) >> 1)
		if !f(h) {
			i = h + 1
		} else {
			j = h
		}
	}
	return i
}

func (s *StreamStore) Categories(kind string) []CatalogCategory {
	if err := s.ensureLoaded(); err != nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	wantKind := catalogKind(kind)
	out := make([]CatalogCategory, 0, s.header.categoryCount)
	for i := range s.header.categoryCount {
		pos := s.header.categoriesOff + uint64(i)*categoryRecordLen
		if s.index[pos+12] != wantKind {
			continue
		}
		recordID := binary.LittleEndian.Uint32(s.index[pos+8:])
		rec, err := s.record(recordID)
		if err != nil {
			continue
		}
		entry, err := decodeCatalogEntry(rec)
		if err != nil {
			continue
		}
		out = append(out, CatalogCategory{
			ID:   binary.LittleEndian.Uint64(s.index[pos:]),
			Name: strings.Clone(entry.Group),
			Type: catalogKindName(wantKind),
		})
	}
	return out
}

func (s *StreamStore) FindStream(id uint64) *CatalogEntry {
	if err := s.ensureLoaded(); err != nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	start, end := s.lookupRange(s.header.streamIDOff, id)
	if start == end {
		return nil
	}
	rec, err := s.record(s.lookupRecordID(s.header.streamIDOff, start))
	if err != nil {
		return nil
	}
	entry, err := decodeCatalogEntry(rec)
	if err != nil {
		return nil
	}
	entry = cloneCatalogEntry(entry)
	entry.Slug = base64.RawURLEncoding.EncodeToString(rec[:28])
	entry.BasePath = "stream"
	return &entry
}

func (s *StreamStore) RangeSeries(categoryID uint64, yield func(int, CatalogSeriesEntry) bool) error {
	if err := s.ensureLoaded(); err != nil {
		return err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	position := 0
	for i := range s.header.seriesCount {
		orderPos := s.header.seriesOrderOff + uint64(i)*4
		seriesPos := binary.LittleEndian.Uint32(s.index[orderPos:])
		metaPos := s.header.seriesOff + uint64(seriesPos)*seriesRecordLen
		catID := binary.LittleEndian.Uint64(s.index[metaPos+12:])
		if categoryID != 0 && catID != categoryID {
			continue
		}
		recordID := binary.LittleEndian.Uint32(s.index[metaPos+8:])
		rec, err := s.record(recordID)
		if err != nil {
			continue
		}
		entry, err := decodeCatalogEntry(rec)
		if err != nil {
			continue
		}
		position++
		if !yield(position, CatalogSeriesEntry{
			SeriesID:   binary.LittleEndian.Uint64(s.index[metaPos:]),
			CategoryID: catID,
			Name:       entry.Show,
			Group:      entry.Group,
			Cover:      entry.Logo,
		}) {
			break
		}
	}
	return nil
}

func (s *StreamStore) SeriesInfo(id uint64) *CatalogSeriesEntry {
	if err := s.ensureLoaded(); err != nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	count := int(s.header.seriesCount)
	pos := sortSearch(count, func(i int) bool {
		return binary.LittleEndian.Uint64(s.index[s.header.seriesOff+uint64(i)*seriesRecordLen:]) >= id
	})
	if pos == count {
		return nil
	}
	metaPos := s.header.seriesOff + uint64(pos)*seriesRecordLen
	if binary.LittleEndian.Uint64(s.index[metaPos:]) != id {
		return nil
	}
	recordID := binary.LittleEndian.Uint32(s.index[metaPos+8:])
	rec, err := s.record(recordID)
	if err != nil {
		return nil
	}
	first, err := decodeCatalogEntry(rec)
	if err != nil {
		return nil
	}
	out := &CatalogSeriesEntry{
		SeriesID:   id,
		CategoryID: binary.LittleEndian.Uint64(s.index[metaPos+12:]),
		Name:       strings.Clone(first.Show),
		Group:      strings.Clone(first.Group),
		Cover:      strings.Clone(first.Logo),
		Episodes:   make(map[int][]CatalogEntry),
	}
	start := binary.LittleEndian.Uint32(s.index[metaPos+20:])
	n := binary.LittleEndian.Uint32(s.index[metaPos+24:])
	if uint64(start)+uint64(n) > uint64(s.header.episodeCount) {
		return nil
	}
	for i := range n {
		episodePos := s.header.episodesOff + uint64(start+i)*episodeRecordLen
		recordID := binary.LittleEndian.Uint32(s.index[episodePos+8:])
		rec, err := s.record(recordID)
		if err != nil {
			return nil
		}
		entry, err := decodeCatalogEntry(rec)
		if err != nil {
			return nil
		}
		entry = cloneCatalogEntry(entry)
		out.Episodes[entry.Season] = append(out.Episodes[entry.Season], entry)
	}
	return out
}
