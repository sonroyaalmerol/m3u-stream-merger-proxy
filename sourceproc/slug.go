package sourceproc

import (
	"crypto/sha3"
	"encoding/base64"
)

func EncodeSlug(stream *StreamInfo) string {
	h := sha3.Sum224([]byte(stream.Title))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func DecodeSlug(slug string) (*StreamInfo, error) {
	return defaultStore.Get(slug)
}
