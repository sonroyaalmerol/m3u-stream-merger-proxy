package handlers

import (
	"errors"
	"testing"

	"m3u-stream-merger/logger"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ldapTestAuth(t *testing.T, bind func(serverURL, dn, password string) error) *CredentialsAuth {
	t.Helper()
	t.Setenv("CREDENTIALS", "")
	t.Setenv("LDAP_URL", "ldap://ldap.test:389")
	t.Setenv("LDAP_BIND_DN", "uid=%s,ou=people,dc=test")
	auth := NewCredentialsAuth(logger.Default)
	auth.bind = bind
	return auth
}

func TestLDAPAuthorize(t *testing.T) {
	var calls int
	var gotURL, gotDN, gotPass string
	auth := ldapTestAuth(t, func(serverURL, dn, password string) error {
		calls++
		gotURL, gotDN, gotPass = serverURL, dn, password
		if password != "secret" {
			return errors.New("invalid credentials")
		}
		return nil
	})

	require.True(t, auth.Authorize("alice", "secret"))
	assert.Equal(t, "ldap://ldap.test:389", gotURL)
	assert.Equal(t, "uid=alice,ou=people,dc=test", gotDN)
	assert.Equal(t, "secret", gotPass)

	assert.False(t, auth.Authorize("alice", "wrong"))
	assert.Equal(t, 2, calls)

	require.True(t, auth.Authorize("alice", "secret"))
	assert.False(t, auth.Authorize("alice", "wrong"))
	assert.Equal(t, 2, calls)
}

func TestLDAPRejectsEmptyPassword(t *testing.T) {
	auth := ldapTestAuth(t, func(string, string, string) error {
		t.Fatal("empty password must never reach the server: an empty simple bind succeeds as anonymous")
		return nil
	})

	assert.False(t, auth.Authorize("alice", ""))
	assert.False(t, auth.Authorize("", "secret"))
}

func TestLDAPStaticCredentialsStillWin(t *testing.T) {
	auth := ldapTestAuth(t, func(string, string, string) error { return errors.New("no such user") })
	t.Setenv("CREDENTIALS", "local:pass")

	assert.True(t, auth.Authorize("local", "pass"))
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

func TestLDAPMissingBindDNRejects(t *testing.T) {
	auth := ldapTestAuth(t, func(string, string, string) error { return nil })
	t.Setenv("LDAP_BIND_DN", "")

	assert.False(t, auth.Authorize("alice", "secret"))
}

func TestAuthorizeOpenWhenNothingConfigured(t *testing.T) {
	t.Setenv("CREDENTIALS", "")
	t.Setenv("LDAP_URL", "")
	auth := NewCredentialsAuth(logger.Default)

	assert.True(t, auth.Authorize("anyone", "anything"))
}
