package handlers

import (
	"net/http"
	"os"
	"strings"
	"time"

	"m3u-stream-merger/logger"
)

// CredentialsAuth validates Xtream-style username/password pairs against the
// CREDENTIALS env (user:pass[[:expiry]]|user:pass...). Empty or "none" disables auth.
type CredentialsAuth struct {
	logger logger.Logger
}

func NewCredentialsAuth(logger logger.Logger) *CredentialsAuth {
	return &CredentialsAuth{logger: logger}
}

func (a *CredentialsAuth) Authorize(user, pass string) bool {
	credentials := os.Getenv("CREDENTIALS")
	if credentials == "" || strings.ToLower(credentials) == "none" {
		return true
	}

	for _, cred := range a.parseCredentials(credentials) {
		if strings.EqualFold(user, cred[0]) && strings.EqualFold(pass, cred[1]) {
			return true
		}
	}
	return false
}

func (a *CredentialsAuth) parseCredentials(raw string) [][]string {
	var result [][]string
	for item := range strings.SplitSeq(raw, "|") {
		cred := strings.Split(item, ":")
		if len(cred) == 3 {
			if d, err := time.ParseInLocation(time.DateOnly, cred[2], time.Local); err != nil {
				a.logger.Warnf("invalid credential format: %s", item)
				continue
			} else if time.Now().After(d) {
				a.logger.Debugf("Credential expired: %s", item)
				continue
			}
			result = append(result, cred[:2])
		} else {
			result = append(result, cred)
		}
	}
	return result
}

func (a *CredentialsAuth) AuthorizeRequest(r *http.Request) bool {
	return a.Authorize(r.URL.Query().Get("username"), r.URL.Query().Get("password"))
}
