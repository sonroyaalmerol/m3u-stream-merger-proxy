package handlers

import (
	"net/http/httptest"
	"strings"
	"testing"

	"m3u-stream-merger/logger"
	"m3u-stream-merger/utils"
)

type recordingLogger struct {
	logger.Logger
	messages []string
}

func (l *recordingLogger) Warn(message string) {
	l.messages = append(l.messages, message)
}

func (l *recordingLogger) Debug(message string) {
	l.messages = append(l.messages, message)
}

func TestCredentialGuards(t *testing.T) {
	t.Setenv("CREDENTIALS", "user:pass|bad user:pass|:x|u:"+strings.Repeat("a", 256)+"|v:p%2Fq")

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

func TestCredentialLogsDoNotContainSecrets(t *testing.T) {
	log := &recordingLogger{}
	auth := NewCredentialsAuth(log)
	auth.parseCredentials("do-not-log:password:not-a-date|expired:password:2000-01-01")

	for _, message := range log.messages {
		if strings.Contains(message, "do-not-log") || strings.Contains(message, "password") {
			t.Fatalf("credential leaked in log message %q", message)
		}
	}
}
