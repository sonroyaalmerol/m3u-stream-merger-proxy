package handlers

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"m3u-stream-merger/logger"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func allowTestAuth(t *testing.T, ddns map[string][]string) *CredentialsAuth {
	t.Helper()
	t.Setenv("CREDENTIALS", "u:p")
	t.Setenv("LDAP_URL", "")
	t.Setenv("TRUSTED_PROXIES", "")
	t.Setenv("ALLOWED_NETWORKS", "")

	auth := NewCredentialsAuth(logger.Default)
	auth.lookup = func(host string) ([]netip.Addr, error) {
		var addrs []netip.Addr
		for _, raw := range ddns[host] {
			addrs = append(addrs, netip.MustParseAddr(raw))
		}
		return addrs, nil
	}
	return auth
}

func allowRequest(remoteAddr string, headers map[string]string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/playlist.m3u", nil)
	req.RemoteAddr = remoteAddr
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req
}

func TestAllowedNetworksDDNS(t *testing.T) {
	auth := allowTestAuth(t, map[string][]string{"home.duckdns.org": {"203.0.113.7"}})
	t.Setenv("ALLOWED_NETWORKS", "home.duckdns.org")

	assert.True(t, auth.AuthorizeRequest(allowRequest("203.0.113.7:51000", nil)))
	assert.False(t, auth.AuthorizeRequest(allowRequest("198.51.100.9:51000", nil)))

	req := allowRequest("198.51.100.9:51000", nil)
	req.URL.RawQuery = "username=u&password=p"
	assert.True(t, auth.AuthorizeRequest(req))
}

func TestAllowedNetworksCIDRAndIPv6Prefix(t *testing.T) {
	auth := allowTestAuth(t, map[string][]string{"v6.example.net": {"2001:db8:1:2::5"}})
	t.Setenv("ALLOWED_NETWORKS", "192.168.1.0/24|v6.example.net")

	assert.True(t, auth.AuthorizeRequest(allowRequest("192.168.1.55:4000", nil)))
	assert.False(t, auth.AuthorizeRequest(allowRequest("192.168.2.55:4000", nil)))

	assert.True(t, auth.AuthorizeRequest(allowRequest("[2001:db8:1:2:dead:beef::1]:4000", nil)))
	assert.False(t, auth.AuthorizeRequest(allowRequest("[2001:db8:1:3::5]:4000", nil)))
}

func TestAllowedNetworksIgnoresUntrustedForwardedFor(t *testing.T) {
	auth := allowTestAuth(t, nil)
	t.Setenv("ALLOWED_NETWORKS", "203.0.113.7")

	spoofed := allowRequest("198.51.100.9:51000", map[string]string{"X-Forwarded-For": "203.0.113.7"})
	assert.False(t, auth.AuthorizeRequest(spoofed), "X-Forwarded-For from an untrusted peer must not grant access")
}

func TestAllowedNetworksTrustsConfiguredProxy(t *testing.T) {
	auth := allowTestAuth(t, nil)
	t.Setenv("ALLOWED_NETWORKS", "203.0.113.7")
	t.Setenv("TRUSTED_PROXIES", "172.18.0.0/16")

	forwarded := allowRequest("172.18.0.5:51000", map[string]string{"X-Forwarded-For": "203.0.113.7"})
	require.True(t, auth.AuthorizeRequest(forwarded))

	wrong := allowRequest("172.18.0.5:51000", map[string]string{"X-Forwarded-For": "198.51.100.9"})
	assert.False(t, auth.AuthorizeRequest(wrong))

	chained := allowRequest("172.18.0.5:51000", map[string]string{"X-Forwarded-For": "203.0.113.7, 198.51.100.9"})
	assert.False(t, auth.AuthorizeRequest(chained))
}

func TestAllowedNetworksUnsetChangesNothing(t *testing.T) {
	auth := allowTestAuth(t, nil)

	assert.False(t, auth.AuthorizeRequest(allowRequest("203.0.113.7:51000", nil)))
	req := allowRequest("203.0.113.7:51000", nil)
	req.URL.RawQuery = "username=u&password=p"
	assert.True(t, auth.AuthorizeRequest(req))
}

func TestAllowedNetworksKeepsLastAnswerWhenLookupFails(t *testing.T) {
	auth := allowTestAuth(t, map[string][]string{"home.duckdns.org": {"203.0.113.7"}})
	t.Setenv("ALLOWED_NETWORKS", "home.duckdns.org")
	t.Setenv("ALLOWED_NETWORKS_REFRESH_SECONDS", "1")
	require.True(t, auth.AuthorizeRequest(allowRequest("203.0.113.7:51000", nil)))

	auth.lookup = func(string) ([]netip.Addr, error) { return nil, assert.AnError }
	auth.allow.refreshed = auth.allow.refreshed.Add(-time.Hour)
	assert.True(t, auth.AuthorizeRequest(allowRequest("203.0.113.7:51000", nil)),
		"a DNS outage must not lock out an already-known address")
}
