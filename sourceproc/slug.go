package sourceproc

import (
	"crypto/sha3"
	"encoding/base64"
	"encoding/binary"
)

func slugSum(title string) [28]byte {
	return sha3.Sum224([]byte(title))
}

// slugParts derives the store key and the slug bytes from one hash of the title.
func slugParts(title string) (uint64, [28]byte) {
	h := slugSum(title)
	return binary.BigEndian.Uint64(h[:8]), h
}

func EncodeSlug(stream *StreamInfo) string {
	h := slugSum(stream.Title)
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func DecodeSlug(slug string) (*StreamInfo, error) {
	return defaultStore.Get(slug)
}
