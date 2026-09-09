package sourceproc

import (
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/unix"
)

type mapping struct {
	path string
	file *os.File
	raw  []byte
}

func (m *mapping) release() {
	if m == nil {
		return
	}
	if m.raw != nil {
		_ = unix.Munmap(m.raw)
		m.raw = nil
	}
	if m.file != nil {
		_ = m.file.Close()
		m.file = nil
	}
	if m.path != "" {
		_ = os.Remove(m.path)
		m.path = ""
	}
}

// mapSlice returns an empty slice of capacity n backed by a sparse temp file
// mapped MAP_SHARED, so its pages are reclaimable and invisible to GOMEMLIMIT.
// T must be pointer-free: the region is not GC-scanned, so a pointer stored in
// it would not keep its target alive. Returns (nil, nil) to mean use the heap.
func mapSlice[T any](path string, n int) ([]T, *mapping) {
	var zero T
	elem := int(unsafe.Sizeof(zero))
	if n <= 0 || elem <= 0 || n > (1<<62)/elem {
		return nil, nil
	}
	file, err := os.Create(path)
	if err != nil {
		return nil, nil
	}
	size := elem * n
	if err := file.Truncate(int64(size)); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, nil
	}
	raw, err := unix.Mmap(int(file.Fd()), 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, nil
	}
	head := (*T)(unsafe.Pointer(unsafe.SliceData(raw)))
	return unsafe.Slice(head, n)[:0], &mapping{path: path, file: file, raw: raw}
}

func (w *StreamStoreWriter) buildPath(name string) string {
	return filepath.Join(storeDir(), fmt.Sprintf("g%d.%s.build", w.gen, name))
}

func mapIndex[T any](w *StreamStoreWriter, name string, n int) []T {
	entries, m := mapSlice[T](w.buildPath(name), n)
	if m == nil {
		return make([]T, 0, n)
	}
	w.mappings = append(w.mappings, m)
	return entries
}

func (w *StreamStoreWriter) releaseMappings() {
	for _, m := range w.mappings {
		m.release()
	}
	w.mappings = nil
}

func removeStaleBuildFiles() {
	entries, err := os.ReadDir(storeDir())
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".build" {
			_ = os.Remove(filepath.Join(storeDir(), entry.Name()))
		}
	}
}
