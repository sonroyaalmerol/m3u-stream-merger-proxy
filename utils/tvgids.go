package utils

import (
	"bufio"
	"encoding/binary"
	"os"
	"slices"

	"github.com/cespare/xxhash"
)

const tvgIDsMagic = "TVGH0001"

// TvgIDHash returns the 64-bit fingerprint stored for a tvg-id.
func TvgIDHash(id string) uint64 {
	return xxhash.Sum64String(id)
}

type TvgIDFilter []uint64

// Has reports whether id is in the filter. A nil filter matches everything.
func (f TvgIDFilter) Has(id string) bool {
	if f == nil {
		return true
	}
	_, ok := slices.BinarySearch(f, TvgIDHash(id))
	return ok
}

// WriteTvgIDHashes sorts, dedupes, and persists hashes to path. The input slice
// is sorted in place.
func WriteTvgIDHashes(path string, hashes []uint64) error {
	slices.Sort(hashes)
	hashes = slices.Compact(hashes)

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	w := bufio.NewWriterSize(f, 64<<10)
	if _, err := w.WriteString(tvgIDsMagic); err != nil {
		return err
	}
	var buf [8]byte
	for _, h := range hashes {
		binary.LittleEndian.PutUint64(buf[:], h)
		if _, err := w.Write(buf[:]); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return f.Sync()
}

// LoadTvgIDFilter reads a filter written by WriteTvgIDHashes. A missing or
// unrecognized file yields nil, which disables filtering.
func LoadTvgIDFilter(path string) TvgIDFilter {
	data, err := os.ReadFile(path)
	if err != nil || len(data) < len(tvgIDsMagic) || string(data[:len(tvgIDsMagic)]) != tvgIDsMagic {
		return nil
	}
	body := data[len(tvgIDsMagic):]
	filter := make(TvgIDFilter, len(body)/8)
	for i := range filter {
		filter[i] = binary.LittleEndian.Uint64(body[i*8:])
	}
	if len(filter) == 0 {
		return nil
	}
	return filter
}
