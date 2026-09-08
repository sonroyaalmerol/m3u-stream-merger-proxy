package utils

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

var HTTPClient = &http.Client{
	Transport: func() *http.Transport {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.MaxIdleConns = 200
		transport.MaxIdleConnsPerHost = 50
		transport.IdleConnTimeout = 120 * time.Second
		transport.TLSHandshakeTimeout = 10 * time.Second
		return transport
	}(),
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		userAgent := GetEnv("USER_AGENT")
		accept := GetEnv("HTTP_ACCEPT")

		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Accept", accept)
		return nil
	},
}

func CustomHttpRequest(ctx context.Context, origReq *http.Request, method string, url string) (*http.Response, error) {
	userAgent := GetEnv("USER_AGENT")
	accept := GetEnv("HTTP_ACCEPT")

	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, err
	}

	origHasUA := false
	origHasAccept := false

	if origReq != nil {
		for header, values := range origReq.Header {
			canonicalHeader := http.CanonicalHeaderKey(header)

			switch canonicalHeader {
			case "User-Agent":
				origHasUA = true
			case "Accept":
				origHasAccept = true
			}

			for _, v := range values {
				req.Header.Add(header, v)
			}
		}
	}

	if !origHasUA {
		req.Header.Set("User-Agent", userAgent)
	}
	if !origHasAccept {
		req.Header.Set("Accept", accept)
	}

	resp, err := HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}

	return resp, nil
}

func DetermineBaseURL(r *http.Request) string {
	if customBase, ok := os.LookupEnv("BASE_URL"); ok {
		return strings.TrimSuffix(customBase, "/")
	}

	if r != nil {
		proto := "http"
		if r.TLS != nil {
			proto = "https"
		} else if IsForwardedHTTPS(r) {
			// ponytail: scheme only, never trust X-Forwarded-Host (spoofable redirect vector)
			proto = "https"
		}
		return fmt.Sprintf("%s://%s", proto, r.Host)
	}

	return ""
}

func IsForwardedHTTPS(r *http.Request) bool {
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}
