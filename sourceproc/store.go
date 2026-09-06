package sourceproc

import (
	"bufio"
	"cmp"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"m3u-stream-merger/config"

	"github.com/goccy/go-json"
)

const (
	indexMagic = "M3USTR01"
	recordLen  = 20
)

// indexEntry: 20 bytes on disk, 24 in memory, so 800k streams index in ~19 MB.
type indexEntry struct {
	key  uint64
	off  uint64
	size uint32
}

// StreamStore: per-generation data+index files, one atomic "current" pointer flip.
type StreamStore struct {
	mu     sync.RWMutex
	gen    uint64
	data   *os.File
	index  []indexEntry
	loaded bool
}

var defaultStore = &StreamStore{}

func storeDir() string    { return config.GetStreamStoreDirPath() }
func currentPath() string { return filepath.Join(storeDir(), "current") }
func dataPath(gen uint64) string {
	return filepath.Join(storeDir(), fmt.Sprintf("g%d.dat", gen))
}
func indexPath(gen uint64) string {
	return filepath.Join(storeDir(), fmt.Sprintf("g%d.idx", gen))
}

// The slug is already a sha3-224 digest, so its leading bytes are the index key.
func slugKey(slug string) (uint64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(slug)
	if err != nil || len(raw) < 8 {
		return 0, fmt.Errorf("invalid slug: %s", slug)
	}

	return binary.BigEndian.Uint64(raw[:8]), nil
}

func (s *StreamStore) Get(slug string) (*StreamInfo, error) {
	key, err := slugKey(slug)
	if err != nil {
		return nil, err
	}

	if err := s.ensureLoaded(); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return legacyDecodeSlug(slug)
		}
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	i, found := slices.BinarySearchFunc(s.index, key, func(e indexEntry, k uint64) int {
		return cmp.Compare(e.key, k)
	})
	if !found {
		return nil, fmt.Errorf("slug not found: %s", slug)
	}

	for ; i < len(s.index) && s.index[i].key == key; i++ {
		buf := make([]byte, s.index[i].size)
		if _, err := s.data.ReadAt(buf, int64(s.index[i].off)); err != nil {
			return nil, fmt.Errorf("reading stream record: %w", err)
		}

		var info StreamInfo
		if err := json.Unmarshal(buf, &info); err != nil {
			continue
		}
		if EncodeSlug(&info) == slug {
			return &info, nil
		}
	}

	return nil, fmt.Errorf("slug not found: %s", slug)
}

func (s *StreamStore) ensureLoaded() error {
	if err := s.loadOnce(); err != nil {
		return err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.loaded {
		return fmt.Errorf("stream store unavailable")
	}

	return nil
}

func (s *StreamStore) loadOnce() error {
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
		return fmt.Errorf("stream store unavailable: %w", err)
	}
	gen, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return fmt.Errorf("stream store pointer corrupt: %w", err)
	}

	idxRaw, err := os.ReadFile(indexPath(gen))
	if err != nil {
		return fmt.Errorf("stream store unavailable: %w", err)
	}
	if len(idxRaw) < len(indexMagic) || string(idxRaw[:len(indexMagic)]) != indexMagic {
		return fmt.Errorf("stream store index corrupt")
	}
	body := idxRaw[len(indexMagic):]
	if rem := len(body) % recordLen; rem != 0 {
		return fmt.Errorf("stream store index corrupt: %d trailing bytes", rem)
	}

	data, err := os.Open(dataPath(gen))
	if err != nil {
		return fmt.Errorf("stream store unavailable: %w", err)
	}

	index := make([]indexEntry, len(body)/recordLen)
	for i := range index {
		rec := body[i*recordLen:]
		index[i] = indexEntry{
			key:  binary.LittleEndian.Uint64(rec),
			off:  binary.LittleEndian.Uint64(rec[8:]),
			size: binary.LittleEndian.Uint32(rec[16:]),
		}
	}

	if s.data != nil {
		_ = s.data.Close()
	}
	s.data = data
	s.index = index
	s.gen = gen
	s.loaded = true

	return nil
}

// Nothing is visible until Commit flips the "current" pointer.
type StreamStoreWriter struct {
	gen   uint64
	file  *os.File
	buf   *bufio.Writer
	off   uint64
	index []indexEntry
}

func NewStreamStoreWriter() (*StreamStoreWriter, error) {
	if err := os.MkdirAll(storeDir(), os.ModePerm); err != nil {
		return nil, err
	}

	gen := uint64(1)
	if raw, err := os.ReadFile(currentPath()); err == nil {
		cur, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("stream store pointer corrupt: %w", err)
		}
		gen = cur + 1
	}

	file, err := os.Create(dataPath(gen))
	if err != nil {
		return nil, err
	}

	return &StreamStoreWriter{
		gen:  gen,
		file: file,
		buf:  bufio.NewWriterSize(file, 1<<20),
	}, nil
}

func (w *StreamStoreWriter) Add(key uint64, stream *StreamInfo) error {
	payload, err := json.Marshal(stream)
	if err != nil {
		return err
	}

	n, err := w.buf.Write(payload)
	if err != nil {
		return err
	}

	w.index = append(w.index, indexEntry{key: key, off: w.off, size: uint32(n)})
	w.off += uint64(n)

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

	slices.SortFunc(w.index, func(a, b indexEntry) int {
		return cmp.Compare(a.key, b.key)
	})

	idxFile, err := os.Create(indexPath(w.gen))
	if err != nil {
		return err
	}
	if err := writeIndex(idxFile, w.index); err != nil {
		_ = idxFile.Close()
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

func writeIndex(f *os.File, index []indexEntry) error {
	out := bufio.NewWriterSize(f, 1<<20)
	if _, err := out.WriteString(indexMagic); err != nil {
		return err
	}

	var rec [recordLen]byte
	for _, e := range index {
		binary.LittleEndian.PutUint64(rec[:], e.key)
		binary.LittleEndian.PutUint64(rec[8:], e.off)
		binary.LittleEndian.PutUint32(rec[16:], e.size)
		if _, err := out.Write(rec[:]); err != nil {
			return err
		}
	}
	if err := out.Flush(); err != nil {
		return err
	}

	return f.Sync()
}

func (w *StreamStoreWriter) Discard() {
	_ = w.file.Close()
	_ = os.Remove(dataPath(w.gen))
	_ = os.Remove(indexPath(w.gen))
	_ = os.Remove(currentPath() + ".new")
}

// ponytail: metadata only; no legacy streams/ URL glob, add back if stale playback across upgrade matters.
func legacyDecodeSlug(slug string) (*StreamInfo, error) {
	data, err := os.ReadFile(filepath.Join(config.GetCurrentSlugDirPath(), slug))
	if err != nil {
		return nil, fmt.Errorf("slug not found: %v", err)
	}

	var info StreamInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("error deserializing slug data: %v", err)
	}

	return &info, nil
}

// Drops the per-stream and per-URL file trees the single-file store replaced.
func cleanupLegacyStores() {
	_ = os.RemoveAll(config.GetStreamsDirPath())
	_ = os.RemoveAll(config.GetCurrentSlugDirPath())
	_ = os.RemoveAll(config.GetNewSlugDirPath())
}
