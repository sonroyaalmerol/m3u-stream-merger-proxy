package logger

import "testing"

// BenchmarkDebugfDisabled covers DEBUG off, the per-chunk production case.
func BenchmarkDebugfDisabled(b *testing.B) {
	b.Setenv("DEBUG", "false")
	l := &DefaultLogger{}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.Debugf("Write: chunk seq=%d size=%d status=%d", int64(i), 65536, 0)
	}
}

// BenchmarkDebugDisabled covers the no-argument variant, also called per chunk.
func BenchmarkDebugDisabled(b *testing.B) {
	b.Setenv("DEBUG", "false")
	l := &DefaultLogger{}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.Debug("Write: Advanced buffer position")
	}
}

func BenchmarkSafeLogfRedacting(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = safeLogf("Proxying %s to %s", "/p/stream/abc", "http://example.com/live/1.ts")
	}
}
