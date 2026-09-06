package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"golang.org/x/crypto/acme/autocert"

	"m3u-stream-merger/logger"
)

// tlsSetup describes how the main listener serves: static cert pair, autocert
// via TLS_DOMAIN, or plain HTTP when useTLS is false.
type tlsSetup struct {
	srv    *http.Server
	cert   string
	key    string
	useTLS bool
	port80 http.Handler
	logger logger.Logger
}

// newTLSSetup builds the main server from TLS_CERT_FILE/TLS_KEY_FILE or
// TLS_DOMAIN (autocert); a non-nil port80 handler means TLS is on.
func newTLSSetup(l logger.Logger) (*tlsSetup, error) {
	cert, key := os.Getenv("TLS_CERT_FILE"), os.Getenv("TLS_KEY_FILE")
	domain := os.Getenv("TLS_DOMAIN")

	s := &tlsSetup{
		srv:    &http.Server{Addr: fmt.Sprintf(":%s", os.Getenv("PORT")), Handler: http.DefaultServeMux},
		logger: l,
	}

	switch {
	case cert != "" || key != "":
		if cert == "" || key == "" {
			return nil, errors.New("TLS_CERT_FILE and TLS_KEY_FILE must be set together")
		}
		s.cert, s.key, s.useTLS = cert, key, true
		l.Logf("TLS enabled (certificate: %s)", cert)
	case domain != "":
		cache := os.Getenv("TLS_CACHE_DIR")
		if cache == "" {
			cache = "certs"
		}
		m := &autocert.Manager{
			Cache:      autocert.DirCache(cache),
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(strings.Split(domain, ",")...),
		}
		s.srv.TLSConfig = &tls.Config{GetCertificate: m.GetCertificate}
		s.useTLS = true
		s.port80 = m.HTTPHandler(s.redirectHandler())
		l.Logf("TLS enabled (autocert for: %s, cache: %s)", domain, cache)
	default:
		return s, nil
	}

	if s.port80 == nil {
		s.port80 = s.redirectHandler()
	}
	return s, nil
}

// serve starts the main listener, plus the :80 redirect when TLS is on.
func (s *tlsSetup) serve() error {
	if s.useTLS {
		go func() {
			l := &http.Server{Addr: ":80", Handler: s.port80}
			if err := l.ListenAndServe(); err != nil {
				s.logger.Warnf("HTTP listener on :80 unavailable (ACME/redirect disabled): %v", err)
			}
		}()
		return s.srv.ListenAndServeTLS(s.cert, s.key)
	}
	return s.srv.ListenAndServe()
}

// redirectHandler sends plain-HTTP traffic to HTTPS, preferring BASE_URL
// (the externally visible URL) when configured.
func (s *tlsSetup) redirectHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := "https://" + r.Host
		if base := os.Getenv("BASE_URL"); base != "" {
			target = strings.TrimSuffix(base, "/") + r.URL.RequestURI()
		}
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	})
}
