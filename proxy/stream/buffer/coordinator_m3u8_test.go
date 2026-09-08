package buffer

import (
	"bytes"
	"testing"
)

func TestReadPlaylistRejectsOversizedBody(t *testing.T) {
	_, err := readPlaylist(bytes.NewReader(make([]byte, maxPlaylistBytes+1)))
	if err == nil {
		t.Fatal("readPlaylist accepted an oversized playlist")
	}
}
