package handlers

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-ldap/ldap/v3"
)

const (
	defaultLDAPCacheTTL = 5 * time.Minute
	ldapFailureTTL      = 30 * time.Second
	ldapCacheMaxEntries = 1000
)

type ldapVerdict struct {
	ok        bool
	expiresAt time.Time
}

// ldapCache keeps bind results so a segment-per-second stream does not bind on every request.
type ldapCache struct {
	mu      sync.Mutex
	entries map[string]ldapVerdict
}

func ldapEnabled() bool { return os.Getenv("LDAP_URL") != "" }

// ldapBindDN renders LDAP_BIND_DN by substituting the escaped username for %s or {username}.
func ldapBindDN(user string) string {
	template := os.Getenv("LDAP_BIND_DN")
	if template == "" || user == "" {
		return ""
	}
	escaped := ldap.EscapeDN(user)
	if strings.Contains(template, "{username}") {
		return strings.ReplaceAll(template, "{username}", escaped)
	}
	return strings.ReplaceAll(template, "%s", escaped)
}

func ldapCacheTTL() time.Duration {
	if v := os.Getenv("LDAP_CACHE_TTL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return defaultLDAPCacheTTL
}

func ldapCacheKey(user, pass string) string {
	sum := sha256.Sum256([]byte(user + "\x00" + pass))
	return hex.EncodeToString(sum[:])
}

func (c *ldapCache) lookup(key string) (bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || time.Now().After(entry.expiresAt) {
		return false, false
	}
	return entry.ok, true
}

func (c *ldapCache) store(key string, ok bool, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]ldapVerdict)
	}
	if len(c.entries) >= ldapCacheMaxEntries {
		now := time.Now()
		for k, v := range c.entries {
			if now.After(v.expiresAt) {
				delete(c.entries, k)
			}
		}
	}
	c.entries[key] = ldapVerdict{ok: ok, expiresAt: time.Now().Add(ttl)}
}

// ldapBind performs the real simple bind; CredentialsAuth.bind swaps it out in tests.
func ldapBind(serverURL, dn, password string) error {
	opts := []ldap.DialOpt{}
	if strings.EqualFold(os.Getenv("LDAP_TLS_SKIP_VERIFY"), "true") {
		opts = append(opts, ldap.DialWithTLSConfig(&tls.Config{InsecureSkipVerify: true}))
	}
	conn, err := ldap.DialURL(serverURL, opts...)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	if strings.EqualFold(os.Getenv("LDAP_START_TLS"), "true") {
		skip := strings.EqualFold(os.Getenv("LDAP_TLS_SKIP_VERIFY"), "true")
		if err := conn.StartTLS(&tls.Config{InsecureSkipVerify: skip}); err != nil {
			return err
		}
	}
	return conn.Bind(dn, password)
}

// authorizeLDAP binds as the user; an empty password is refused because servers answer that with an anonymous bind.
func (a *CredentialsAuth) authorizeLDAP(user, pass string) bool {
	serverURL := os.Getenv("LDAP_URL")
	if serverURL == "" || user == "" || pass == "" {
		return false
	}
	dn := ldapBindDN(user)
	if dn == "" {
		a.logger.Warn("LDAP_URL is set but LDAP_BIND_DN is empty, rejecting login")
		return false
	}

	key := ldapCacheKey(user, pass)
	if ok, found := a.ldap.lookup(key); found {
		return ok
	}

	bind := a.bind
	if bind == nil {
		bind = ldapBind
	}
	if err := bind(serverURL, dn, pass); err != nil {
		a.logger.Warnf("LDAP bind failed for %s: %v", dn, err)
		a.ldap.store(key, false, ldapFailureTTL)
		return false
	}

	a.logger.Debugf("LDAP bind succeeded for %s", dn)
	a.ldap.store(key, true, ldapCacheTTL())
	return true
}
