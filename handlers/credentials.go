package handlers

import (
	"net/http"
	"net/url"
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
		if user == cred[0] && pass == cred[1] {
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
				a.logger.Warn("invalid credential expiry")
				continue
			} else if time.Now().After(d) {
				a.logger.Debug("credential expired")
				continue
			}
			cred = cred[:2]
		}
		if !validCredentialPair(cred) {
			a.logger.Warn("skipping unsafe or empty credential")
			continue
		}
		result = append(result, cred)
	}
	return result
}

// validCredentialPair enforces URL-safe printable ASCII (survives paths and queries verbatim), max 255 chars per part.
func validCredentialPair(cred []string) bool {
	if len(cred) < 2 || cred[0] == "" || cred[1] == "" {
		return false
	}
	for _, part := range cred[:2] {
		if len(part) > 255 {
			return false
		}
		for i := 0; i < len(part); i++ {
			c := part[i]
			switch {
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			case c == '-' || c == '.' || c == '_' || c == '~' || c == '!' || c == '$' || c == '\'' || c == '(' || c == ')' || c == '*' || c == ',' || c == ';' || c == '=' || c == '@':
			default:
				return false
			}
		}
	}
	return true
}

func (a *CredentialsAuth) AuthorizeRequest(r *http.Request) bool {
	values := RequestValues(r)

	return a.Authorize(values.Get("username"), values.Get("password"))
}

// RequestValues merges query and POST form; panels accept credentials either way.
func RequestValues(r *http.Request) url.Values {
	if err := r.ParseForm(); err != nil {
		return r.URL.Query()
	}

	return r.Form
}
