package utils

import "net/http"

func CustomHttpRequest(method string, url string, rangeHeader string) (*http.Response, error) {
	userAgent := GetEnv("USER_AGENT")

	// Create a new HTTP client with a custom User-Agent header
	client := &http.Client{}

	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", userAgent)

	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	return resp, nil
}
