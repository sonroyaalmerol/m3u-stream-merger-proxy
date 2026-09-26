package handlers

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
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
	defaultUserFilter   = "(uid=%s)"
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

// ldapConn is the slice of *ldap.Conn this package uses, so tests can substitute a fake directory.
type ldapConn interface {
	Bind(username, password string) error
	Search(request *ldap.SearchRequest) (*ldap.SearchResult, error)
	Close() error
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

func ldapUserFilter(user string) string {
	filter := os.Getenv("LDAP_USER_FILTER")
	if filter == "" {
		filter = defaultUserFilter
	}
	escaped := ldap.EscapeFilter(user)
	if strings.Contains(filter, "{username}") {
		return strings.ReplaceAll(filter, "{username}", escaped)
	}
	return strings.ReplaceAll(filter, "%s", escaped)
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

// dialLDAP opens the connection CredentialsAuth.dial replaces in tests.
func dialLDAP(serverURL string) (ldapConn, error) {
	skipVerify := strings.EqualFold(os.Getenv("LDAP_TLS_SKIP_VERIFY"), "true")
	opts := []ldap.DialOpt{}
	if skipVerify {
		opts = append(opts, ldap.DialWithTLSConfig(&tls.Config{InsecureSkipVerify: true}))
	}
	conn, err := ldap.DialURL(serverURL, opts...)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(os.Getenv("LDAP_START_TLS"), "true") {
		if err := conn.StartTLS(&tls.Config{InsecureSkipVerify: skipVerify}); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}
	return conn, nil
}

// authorizeLDAP binds as the user; an empty password is refused because servers answer that with an anonymous bind.
func (a *CredentialsAuth) authorizeLDAP(user, pass string) bool {
	serverURL := os.Getenv("LDAP_URL")
	if serverURL == "" || user == "" || pass == "" {
		return false
	}

	key := ldapCacheKey(user, pass)
	if ok, found := a.ldap.lookup(key); found {
		return ok
	}

	ok := a.ldapLogin(serverURL, user, pass)
	ttl := ldapFailureTTL
	if ok {
		ttl = ldapCacheTTL()
	}
	a.ldap.store(key, ok, ttl)
	return ok
}

func (a *CredentialsAuth) ldapLogin(serverURL, user, pass string) bool {
	dial := a.dial
	if dial == nil {
		dial = dialLDAP
	}
	conn, err := dial(serverURL)
	if err != nil {
		a.logger.Warnf("LDAP dial %s failed: %v", serverURL, err)
		return false
	}
	defer func() { _ = conn.Close() }()

	dn, memberOf, err := resolveUser(conn, user)
	if err != nil {
		a.logger.Warnf("LDAP lookup for %q failed: %v", user, err)
		return false
	}

	if err := conn.Bind(dn, pass); err != nil {
		a.logger.Warnf("LDAP bind failed for %s: %v", dn, err)
		return false
	}

	if group := os.Getenv("LDAP_REQUIRED_GROUP"); group != "" {
		if err := serviceBind(conn); err != nil {
			a.logger.Warnf("LDAP service account re-bind failed: %v", err)
			return false
		}
		if !inLDAPGroup(conn, dn, user, group, memberOf) {
			a.logger.Warnf("LDAP user %s is not a member of %s", dn, group)
			return false
		}
	}

	a.logger.Debugf("LDAP bind succeeded for %s", dn)
	return true
}

// serviceBind authenticates as LDAP_BIND_USER; a no-op when no service account is configured.
func serviceBind(conn ldapConn) error {
	svcUser := os.Getenv("LDAP_BIND_USER")
	if svcUser == "" {
		return nil
	}
	if err := conn.Bind(svcUser, os.Getenv("LDAP_BIND_PASSWORD")); err != nil {
		return fmt.Errorf("service account bind: %w", err)
	}
	return nil
}

// resolveUser searches under LDAP_BASE_DN when set, otherwise renders the LDAP_BIND_DN template, and returns any memberOf it already saw.
func resolveUser(conn ldapConn, user string) (string, []string, error) {
	baseDN := os.Getenv("LDAP_BASE_DN")
	if baseDN == "" {
		dn := ldapBindDN(user)
		if dn == "" {
			return "", nil, fmt.Errorf("set LDAP_BIND_DN or LDAP_BASE_DN")
		}
		return dn, nil, nil
	}

	if err := serviceBind(conn); err != nil {
		return "", nil, err
	}

	result, err := conn.Search(ldap.NewSearchRequest(
		baseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 2, 10, false,
		ldapUserFilter(user), []string{"dn", "memberOf"}, nil,
	))
	if err != nil {
		return "", nil, err
	}
	if len(result.Entries) != 1 {
		return "", nil, fmt.Errorf("expected exactly one entry, got %d", len(result.Entries))
	}
	return result.Entries[0].DN, result.Entries[0].GetAttributeValues("memberOf"), nil
}

// inLDAPGroup checks memberOf on the user entry, falling back to a member lookup on the group for directories without the memberof overlay.
func inLDAPGroup(conn ldapConn, dn, user, group string, known []string) bool {
	if hasGroup(known, group) {
		return true
	}

	result, err := conn.Search(ldap.NewSearchRequest(
		dn, ldap.ScopeBaseObject, ldap.NeverDerefAliases, 0, 10, false,
		"(objectClass=*)", []string{"memberOf"}, nil,
	))
	if err == nil {
		for _, entry := range result.Entries {
			if hasGroup(entry.GetAttributeValues("memberOf"), group) {
				return true
			}
		}
	}

	filter := fmt.Sprintf("(|(member=%s)(uniqueMember=%s)(memberUid=%s))",
		ldap.EscapeFilter(dn), ldap.EscapeFilter(dn), ldap.EscapeFilter(user))
	members, err := conn.Search(ldap.NewSearchRequest(
		group, ldap.ScopeBaseObject, ldap.NeverDerefAliases, 1, 10, false,
		filter, []string{"dn"}, nil,
	))
	return err == nil && len(members.Entries) == 1
}

func hasGroup(values []string, group string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), group) {
			return true
		}
	}
	return false
}
