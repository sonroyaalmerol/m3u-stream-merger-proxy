package utils

import (
	"fmt"
	"net/http"
	"os"
	"strings"
)

var HTTPClient = &http.Client{
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		userAgent := GetEnv("USER_AGENT")

		// Follow redirects while preserving the custom User-Agent header
		req.Header.Set("User-Agent", userAgent)
		return nil
	},
}

func CustomHttpRequest(method string, url string) (*http.Response, error) {
	userAgent := GetEnv("USER_AGENT")

	// Create a new HTTP client with a custom User-Agent header
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", userAgent)

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
		if r.TLS == nil {
			return fmt.Sprintf("http://%s", r.Host)
		} else {
			return fmt.Sprintf("https://%s", r.Host)
		}
	}

	return ""
}
