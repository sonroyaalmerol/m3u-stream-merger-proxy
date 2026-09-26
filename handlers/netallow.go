package handlers

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultAllowRefresh = 60 * time.Second

// netAllowList resolves ALLOWED_NETWORKS (DDNS hostnames, IPs and CIDRs) and caches the result between refreshes.
type netAllowList struct {
	mu        sync.Mutex
	raw       string
	prefixes  []netip.Prefix
	refreshed time.Time
}

func allowRefreshInterval() time.Duration {
	if v := os.Getenv("ALLOWED_NETWORKS_REFRESH_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return defaultAllowRefresh
}

// hostPrefix widens a resolved address to the client's /64, because IPv6 privacy addresses rotate within it.
func hostPrefix(addr netip.Addr) netip.Prefix {
	addr = addr.Unmap()
	if addr.Is6() {
		return netip.PrefixFrom(addr, 64).Masked()
	}
	return netip.PrefixFrom(addr, 32)
}

func parseAllowEntry(entry string, lookup func(string) ([]netip.Addr, error)) []netip.Prefix {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return nil
	}
	if prefix, err := netip.ParsePrefix(entry); err == nil {
		return []netip.Prefix{prefix.Masked()}
	}
	if addr, err := netip.ParseAddr(entry); err == nil {
		return []netip.Prefix{hostPrefix(addr)}
	}

	addrs, err := lookup(entry)
	if err != nil {
		return nil
	}
	prefixes := make([]netip.Prefix, 0, len(addrs))
	for _, addr := range addrs {
		prefixes = append(prefixes, hostPrefix(addr))
	}
	return prefixes
}

func lookupHost(host string) ([]netip.Addr, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

func (l *netAllowList) current(raw string, lookup func(string) ([]netip.Addr, error)) []netip.Prefix {
	l.mu.Lock()
	defer l.mu.Unlock()

	fresh := time.Since(l.refreshed) < allowRefreshInterval()
	if fresh && raw == l.raw {
		return l.prefixes
	}

	var prefixes []netip.Prefix
	for entry := range strings.SplitSeq(raw, "|") {
		prefixes = append(prefixes, parseAllowEntry(entry, lookup)...)
	}
	if len(prefixes) == 0 && raw == l.raw {
		l.refreshed = time.Now()
		return l.prefixes
	}

	l.raw, l.prefixes, l.refreshed = raw, prefixes, time.Now()
	return prefixes
}

// clientAddr is the peer address, replaced by the last X-Forwarded-For hop only when the peer itself is a trusted proxy.
func clientAddr(r *http.Request, trusted []netip.Prefix) (netip.Addr, bool) {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		addr, err := netip.ParseAddr(strings.Trim(r.RemoteAddr, "[]"))
		if err != nil {
			return netip.Addr{}, false
		}
		peer = netip.AddrPortFrom(addr, 0)
	}
	addr := peer.Addr().Unmap()

	if len(trusted) == 0 || !containsAddr(trusted, addr) {
		return addr, true
	}
	forwarded := r.Header.Get("X-Forwarded-For")
	if forwarded == "" {
		return addr, true
	}
	hops := strings.Split(forwarded, ",")
	claimed, err := netip.ParseAddr(strings.TrimSpace(hops[len(hops)-1]))
	if err != nil {
		return addr, true
	}
	return claimed.Unmap(), true
}

func containsAddr(prefixes []netip.Prefix, addr netip.Addr) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func (a *CredentialsAuth) trustedProxies() []netip.Prefix {
	raw := os.Getenv("TRUSTED_PROXIES")
	if raw == "" {
		return nil
	}
	return a.trusted.current(raw, func(string) ([]netip.Addr, error) { return nil, nil })
}

func (a *CredentialsAuth) allowedByNetwork(r *http.Request) bool {
	raw := os.Getenv("ALLOWED_NETWORKS")
	if raw == "" || r == nil {
		return false
	}

	lookup := a.lookup
	if lookup == nil {
		lookup = lookupHost
	}
	allowed := a.allow.current(raw, lookup)
	if len(allowed) == 0 {
		return false
	}

	addr, ok := clientAddr(r, a.trustedProxies())
	if !ok {
		return false
	}
	if !containsAddr(allowed, addr) {
		return false
	}

	a.logger.Debugf("authorized %s via ALLOWED_NETWORKS", addr)
	return true
}
