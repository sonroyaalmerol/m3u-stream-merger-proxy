package logger

import "testing"

func TestSafeLogfAlwaysRedactsURLs(t *testing.T) {
	t.Setenv("SAFE_LOGS", "false")

	got := safeLogf("proxying to %s", "http://user:pass@example.com/live/user/pass/1.ts?token=secret")
	if got != "proxying to [redacted url]" {
		t.Fatalf("safeLogf() = %q, want redacted URL", got)
	}
}
