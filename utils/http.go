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
		accept := GetEnv("HTTP_ACCEPT")

		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Accept", accept)
		return nil
	},
}

func CustomHttpRequest(origReq *http.Request, method string, url string) (*http.Response, error) {
	userAgent := GetEnv("USER_AGENT")
	accept := GetEnv("HTTP_ACCEPT")

	req, err := http.NewRequest(method, url, nil)
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
		if r.TLS == nil {
			return fmt.Sprintf("http://%s", r.Host)
		} else {
			return fmt.Sprintf("https://%s", r.Host)
		}
	}

	return ""
}
