package handlers

import (
	"errors"
	"strings"
	"testing"

	"m3u-stream-merger/logger"

	"github.com/go-ldap/ldap/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeLDAP is a directory with one user, one group and a password per DN.
type fakeLDAP struct {
	passwords map[string]string
	userDN    string
	memberOf  []string
	groupHas  bool
	binds     []string
	searches  []*ldap.SearchRequest
	dials     int
	searchErr error
}

func (f *fakeLDAP) Close() error { return nil }

func (f *fakeLDAP) Bind(dn, password string) error {
	f.binds = append(f.binds, dn)
	if want, ok := f.passwords[dn]; ok && want == password {
		return nil
	}
	return errors.New("invalid credentials")
}

func (f *fakeLDAP) Search(req *ldap.SearchRequest) (*ldap.SearchResult, error) {
	f.searches = append(f.searches, req)
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	switch {
	case req.Scope == ldap.ScopeWholeSubtree:
		if f.userDN == "" {
			return &ldap.SearchResult{}, nil
		}
		return &ldap.SearchResult{Entries: []*ldap.Entry{{DN: f.userDN}}}, nil
	case req.BaseDN == f.userDN:
		return &ldap.SearchResult{Entries: []*ldap.Entry{{
			DN:         f.userDN,
			Attributes: []*ldap.EntryAttribute{{Name: "memberOf", Values: f.memberOf}},
		}}}, nil
	default:
		if f.groupHas {
			return &ldap.SearchResult{Entries: []*ldap.Entry{{DN: req.BaseDN}}}, nil
		}
		return &ldap.SearchResult{}, nil
	}
}

func ldapTestAuth(t *testing.T, dir *fakeLDAP) *CredentialsAuth {
	t.Helper()
	t.Setenv("CREDENTIALS", "")
	t.Setenv("LDAP_URL", "ldap://ldap.test:389")
	t.Setenv("LDAP_BIND_DN", "uid=%s,ou=people,dc=test")
	t.Setenv("LDAP_BASE_DN", "")
	t.Setenv("LDAP_BIND_USER", "")
	t.Setenv("LDAP_REQUIRED_GROUP", "")
	auth := NewCredentialsAuth(logger.Default)
	auth.dial = func(string) (ldapConn, error) {
		dir.dials++
		return dir, nil
	}
	return auth
}

func TestLDAPAuthorizeTemplateDN(t *testing.T) {
	dir := &fakeLDAP{passwords: map[string]string{"uid=alice,ou=people,dc=test": "secret"}}
	auth := ldapTestAuth(t, dir)

	require.True(t, auth.Authorize("alice", "secret"))
	assert.Equal(t, []string{"uid=alice,ou=people,dc=test"}, dir.binds)
	assert.Empty(t, dir.searches, "template mode must not need a search")

	assert.False(t, auth.Authorize("alice", "wrong"))

	require.True(t, auth.Authorize("alice", "secret"))
	assert.False(t, auth.Authorize("alice", "wrong"))
	assert.Equal(t, 2, dir.dials)
}

func TestLDAPSearchThenBind(t *testing.T) {
	dir := &fakeLDAP{
		userDN: "cn=Alice Smith,ou=staff,dc=test",
		passwords: map[string]string{
			"cn=svc,dc=test":                  "svcpass",
			"cn=Alice Smith,ou=staff,dc=test": "secret",
		},
	}
	auth := ldapTestAuth(t, dir)
	t.Setenv("LDAP_BASE_DN", "dc=test")
	t.Setenv("LDAP_USER_FILTER", "(sAMAccountName=%s)")
	t.Setenv("LDAP_BIND_USER", "cn=svc,dc=test")
	t.Setenv("LDAP_BIND_PASSWORD", "svcpass")

	require.True(t, auth.Authorize("alice", "secret"))
	assert.Equal(t, []string{"cn=svc,dc=test", "cn=Alice Smith,ou=staff,dc=test"}, dir.binds)
	require.Len(t, dir.searches, 1)
	assert.Equal(t, "(sAMAccountName=alice)", dir.searches[0].Filter)

	dir.userDN = ""
	assert.False(t, auth.Authorize("ghost", "secret"))
}

func TestLDAPRequiredGroup(t *testing.T) {
	const userDN = "uid=alice,ou=people,dc=test"
	dir := &fakeLDAP{userDN: userDN, passwords: map[string]string{userDN: "secret"}, memberOf: []string{"cn=iptv,ou=groups,dc=test"}}
	auth := ldapTestAuth(t, dir)
	t.Setenv("LDAP_REQUIRED_GROUP", "cn=iptv,ou=groups,dc=test")

	require.True(t, auth.Authorize("alice", "secret"))

	outsider := &fakeLDAP{userDN: userDN, passwords: map[string]string{userDN: "secret"}, memberOf: []string{"cn=other,dc=test"}}
	auth2 := ldapTestAuth(t, outsider)
	t.Setenv("LDAP_REQUIRED_GROUP", "cn=iptv,ou=groups,dc=test")
	assert.False(t, auth2.Authorize("alice", "secret"))

	posix := &fakeLDAP{userDN: userDN, passwords: map[string]string{userDN: "secret"}, groupHas: true}
	auth3 := ldapTestAuth(t, posix)
	t.Setenv("LDAP_REQUIRED_GROUP", "cn=iptv,ou=groups,dc=test")
	assert.True(t, auth3.Authorize("alice", "secret"))
}

func TestLDAPRejectsEmptyPassword(t *testing.T) {
	dir := &fakeLDAP{passwords: map[string]string{"": ""}}
	auth := ldapTestAuth(t, dir)

	assert.False(t, auth.Authorize("alice", ""))
	assert.False(t, auth.Authorize("", "secret"))
	assert.Zero(t, dir.dials, "an empty simple bind succeeds as anonymous, so it must never be sent")
}

func TestLDAPStaticCredentialsStillWin(t *testing.T) {
	dir := &fakeLDAP{}
	auth := ldapTestAuth(t, dir)
	t.Setenv("CREDENTIALS", "local:pass")

	assert.True(t, auth.Authorize("local", "pass"))
	assert.Zero(t, dir.dials)
	assert.False(t, auth.Authorize("local", "nope"))
}

func TestLDAPBindDNTemplates(t *testing.T) {
	t.Setenv("LDAP_BIND_DN", "{username}@corp.test")
	assert.Equal(t, "bob@corp.test", ldapBindDN("bob"))

	t.Setenv("LDAP_BIND_DN", "uid=%s,dc=test")
	assert.Equal(t, `uid=bob\,evil,dc=test`, ldapBindDN("bob,evil"))

	t.Setenv("LDAP_BIND_DN", "")
	assert.Empty(t, ldapBindDN("bob"))
}

func TestLDAPUserFilterEscaping(t *testing.T) {
	t.Setenv("LDAP_USER_FILTER", "")
	assert.Equal(t, "(uid=bob)", ldapUserFilter("bob"))

	t.Setenv("LDAP_USER_FILTER", "(&(objectClass=person)(uid={username}))")
	filter := ldapUserFilter("bob)(uid=*")
	assert.NotContains(t, strings.TrimPrefix(filter, "(&(objectClass=person)(uid="), "(uid=*")
}

func TestLDAPMissingBindDNRejects(t *testing.T) {
	dir := &fakeLDAP{}
	auth := ldapTestAuth(t, dir)
	t.Setenv("LDAP_BIND_DN", "")

	assert.False(t, auth.Authorize("alice", "secret"))
	assert.Empty(t, dir.binds)
}

func TestAuthorizeOpenWhenNothingConfigured(t *testing.T) {
	t.Setenv("CREDENTIALS", "")
	t.Setenv("LDAP_URL", "")
	auth := NewCredentialsAuth(logger.Default)

	assert.True(t, auth.Authorize("anyone", "anything"))
}
