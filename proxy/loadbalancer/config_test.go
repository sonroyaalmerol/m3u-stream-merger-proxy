package loadbalancer

import "testing"

func TestNewDefaultLBConfigHealthSampleIgnoresBufferSize(t *testing.T) {
	t.Setenv("BUFFER_CHUNK_NUM", "32")

	config := NewDefaultLBConfig()
	if config.HealthSampleBytes != maxHealthSampleBytes {
		t.Fatalf("HealthSampleBytes = %d, want %d", config.HealthSampleBytes, maxHealthSampleBytes)
	}
}
