package config

import "testing"

func TestEnablePCRPacerEnabledByDefault(t *testing.T) {
	t.Setenv("ENABLE_PCR_PACER", "")
	if !NewDefaultStreamConfig().EnablePCRPacer {
		t.Fatal("EnablePCRPacer = false by default, want true")
	}

	t.Setenv("ENABLE_PCR_PACER", "false")
	if NewDefaultStreamConfig().EnablePCRPacer {
		t.Fatal("ENABLE_PCR_PACER=false not honored")
	}

	t.Setenv("ENABLE_PCR_PACER", "garbage")
	if !NewDefaultStreamConfig().EnablePCRPacer {
		t.Fatal("invalid ENABLE_PCR_PACER should keep pacer enabled")
	}
}

func TestNewDefaultStreamConfigUsesSixteenChunks(t *testing.T) {
	t.Setenv("BUFFER_CHUNK_NUM", "")
	if got := NewDefaultStreamConfig().SharedBufferSize; got != 16 {
		t.Fatalf("SharedBufferSize = %d, want 16", got)
	}
}

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
