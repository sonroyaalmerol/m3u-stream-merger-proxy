package handlers

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"m3u-stream-merger/logger"
	"m3u-stream-merger/utils"
)

func TestCredentialGuards(t *testing.T) {
	os.Setenv("CREDENTIALS", "user:pass|bad user:pass|:x|u:"+strings.Repeat("a", 256)+"|v:p%2Fq")
	defer os.Unsetenv("CREDENTIALS")

	auth := NewCredentialsAuth(logger.Default)
	if !auth.Authorize("user", "pass") {
		t.Fatal("valid credential should pass")
	}
	rejects := [][2]string{
		{"bad user", "pass"},
		{"", "x"},
		{"u", strings.Repeat("a", 256)},
		{"v", "p%2Fq"},
		{"USER", "pass"},
	}
	for _, c := range rejects {
		if auth.Authorize(c[0], c[1]) {
			t.Fatalf("credential %q/%q should be rejected", c[0], c[1])
		}
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	if !utils.IsForwardedHTTPS(req) {
		t.Fatal("X-Forwarded-Proto https should be detected")
	}
}
