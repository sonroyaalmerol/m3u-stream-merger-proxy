package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"m3u-stream-merger/logger"
)

// genCert writes a self-signed cert/key pair to dir and returns their paths.
func genCert(t *testing.T, dir string) (cert, key string) {
	t.Helper()
	keyObj, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.ParseIP("::1")},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &keyObj.PublicKey, keyObj)
	if err != nil {
		t.Fatal(err)
	}
	cert, key = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	_ = os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600)
	keyDER, _ := x509.MarshalECPrivateKey(keyObj)
	_ = os.WriteFile(key, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600)
	return
}

func TestNewTLSSetupModes(t *testing.T) {
	l := logger.Default

	t.Run("plain", func(t *testing.T) {
		setup, err := newTLSSetup(l)
		if err != nil {
			t.Fatal(err)
		}
		if setup.useTLS || setup.port80 != nil {
			t.Fatalf("expected plain HTTP, got useTLS=%v port80=%v", setup.useTLS, setup.port80 != nil)
		}
	})

	t.Run("file mode", func(t *testing.T) {
		cert, key := genCert(t, t.TempDir())
		t.Setenv("TLS_CERT_FILE", cert)
		t.Setenv("TLS_KEY_FILE", key)
		setup, err := newTLSSetup(l)
		if err != nil {
			t.Fatal(err)
		}
		if !setup.useTLS || setup.cert != cert || setup.key != key || setup.port80 == nil {
			t.Fatalf("bad file-mode setup: %+v", setup)
		}
	})

	t.Run("autocert mode", func(t *testing.T) {
		t.Setenv("TLS_DOMAIN", "example.com")
		setup, err := newTLSSetup(l)
		if err != nil {
			t.Fatal(err)
		}
		if !setup.useTLS || setup.srv.TLSConfig.GetCertificate == nil || setup.port80 == nil {
			t.Fatalf("bad autocert setup: %+v", setup)
		}
	})

	t.Run("mismatched pair", func(t *testing.T) {
		cert, _ := genCert(t, t.TempDir())
		t.Setenv("TLS_CERT_FILE", cert)
		if _, err := newTLSSetup(l); err == nil {
			t.Fatal("expected error for cert without key")
		}
	})
}

func TestServeTLS(t *testing.T) {
	http.HandleFunc("/tls-smoke/", func(w http.ResponseWriter, r *http.Request) {})

	cert, key := genCert(t, t.TempDir())
	t.Setenv("PORT", "0")
	t.Setenv("TLS_CERT_FILE", cert)
	t.Setenv("TLS_KEY_FILE", key)
	setup, err := newTLSSetup(logger.Default)
	if err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = setup.srv.ServeTLS(ln, cert, key) }()
	t.Cleanup(func() { _ = setup.srv.Close() })

	roots := x509.NewCertPool()
	pemBytes, err := os.ReadFile(cert)
	if err != nil {
		t.Fatal(err)
	}
	if !roots.AppendCertsFromPEM(pemBytes) {
		t.Fatal("failed to load test certificate")
	}
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
	}}
	resp, err := client.Get("https://" + ln.Addr().String() + "/tls-smoke/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.TLS == nil {
		t.Fatalf("expected 200 over TLS, got %d TLS=%v", resp.StatusCode, resp.TLS != nil)
	}
}

func TestRedirectHandler(t *testing.T) {
	setup := &tlsSetup{logger: logger.Default}

	rr := httptest.NewRecorder()
	setup.redirectHandler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "http://host.example/foo", nil))
	if rr.Code != http.StatusUpgradeRequired {
		t.Fatalf("expected 426 without BASE_URL, got %d", rr.Code)
	}

	t.Setenv("BASE_URL", "https://base.example:8443/")
	rr = httptest.NewRecorder()
	setup.redirectHandler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "http://host.example/foo?a=1", nil))
	if rr.Code != http.StatusMovedPermanently || rr.Header().Get("Location") != "https://base.example:8443/foo?a=1" {
		t.Fatalf("BASE_URL redirect: %d %s", rr.Code, rr.Header().Get("Location"))
	}

	t.Setenv("BASE_URL", "")
	setup = &tlsSetup{logger: logger.Default, redirect: "https://stream.example.com"}
	rr = httptest.NewRecorder()
	setup.redirectHandler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "http://evil.example/foo", nil))
	if got := rr.Header().Get("Location"); got != "https://stream.example.com/foo" {
		t.Fatalf("TLS_DOMAIN redirect: %s", got)
	}
}
