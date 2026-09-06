package sourceproc

import (
	"crypto/sha3"
	"encoding/base64"
	"encoding/binary"
)

// slugParts derives the store key and the slug from one hash of the title.
func slugParts(title string) (uint64, string) {
	h := sha3.Sum224([]byte(title))
	return binary.BigEndian.Uint64(h[:8]), base64.RawURLEncoding.EncodeToString(h[:])
}

func EncodeSlug(stream *StreamInfo) string {
	_, slug := slugParts(stream.Title)
	return slug
}

func DecodeSlug(slug string) (*StreamInfo, error) {
	return defaultStore.Get(slug)
}
