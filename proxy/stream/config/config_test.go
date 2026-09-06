package config

import "testing"

func TestNewDefaultStreamConfig_ClampsInvalidValues(t *testing.T) {
	t.Setenv("BUFFER_CHUNK_NUM", "0")
	t.Setenv("STREAM_TIMEOUT", "0")

	cfg := NewDefaultStreamConfig()

	if cfg.SharedBufferSize < 2 {
		t.Fatalf("SharedBufferSize = %d, want at least 2 (ring.New(0) returns nil)", cfg.SharedBufferSize)
	}
	if cfg.TimeoutSeconds < 1 {
		t.Fatalf("TimeoutSeconds = %d, want at least 1 (0 yields a negative backoff ceiling)", cfg.TimeoutSeconds)
	}
}
