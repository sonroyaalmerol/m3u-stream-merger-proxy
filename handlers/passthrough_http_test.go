package handlers

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"m3u-stream-merger/logger"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestPassthroughHTTPHandlerRejectsUnsafeTargets(t *testing.T) {
	tests := []struct {
		name   string
		target string
	}{
		{name: "loopback IPv4", target: "http://127.0.0.1/admin"},
		{name: "loopback IPv6", target: "http://[::1]/admin"},
		{name: "private network", target: "http://10.0.0.1/admin"},
		{name: "link-local metadata", target: "http://169.254.169.254/latest/meta-data"},
		{name: "unsupported scheme", target: "file:///etc/passwd"},
		{name: "embedded credentials", target: "https://user:pass@example.com/image.png"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewPassthroughHTTPHandler(logger.Default)
			encoded := base64.URLEncoding.EncodeToString([]byte(tt.target))
			request := httptest.NewRequest(http.MethodGet, "/a/"+encoded, nil)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestPassthroughHTTPHandlerForwardsPublicResourceWithoutCredentials(t *testing.T) {
	handler := NewPassthroughHTTPHandler(logger.Default)
	handler.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://example.com/image.png" {
			t.Fatalf("target = %q, want public image URL", request.URL.String())
		}
		for _, header := range []string{"Authorization", "Cookie", "X-Forwarded-For"} {
			if value := request.Header.Get(header); value != "" {
				t.Fatalf("%s leaked upstream: %q", header, value)
			}
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"image/png"}},
			Body:       io.NopCloser(strings.NewReader("image")),
		}, nil
	})}

	encoded := base64.URLEncoding.EncodeToString([]byte("https://example.com/image.png"))
	request := httptest.NewRequest(http.MethodGet, "/a/"+encoded, nil)
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Cookie", "session=secret")
	request.Header.Set("X-Forwarded-For", "127.0.0.1")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if body := response.Body.String(); body != "image" {
		t.Fatalf("body = %q, want image", body)
	}
}

func TestPassthroughHTTPHandlerRejectsUnsafeMethod(t *testing.T) {
	handler := NewPassthroughHTTPHandler(logger.Default)
	encoded := base64.URLEncoding.EncodeToString([]byte("https://example.com/image.png"))
	request := httptest.NewRequest(http.MethodPost, "/a/"+encoded, strings.NewReader("secret"))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusMethodNotAllowed)
	}
}

func TestIsPublicAddress(t *testing.T) {
	tests := []struct {
		address string
		want    bool
	}{
		{address: "8.8.8.8", want: true},
		{address: "2606:4700:4700::1111", want: true},
		{address: "127.0.0.1", want: false},
		{address: "10.0.0.1", want: false},
		{address: "100.64.0.1", want: false},
		{address: "169.254.169.254", want: false},
		{address: "::1", want: false},
		{address: "fe80::1", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			got := isPublicAddress(netip.MustParseAddr(tt.address))
			if got != tt.want {
				t.Fatalf("isPublicAddress(%q) = %t, want %t", tt.address, got, tt.want)
			}
		})
	}
}
