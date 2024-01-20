package m3u

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

var (
	m3uFilePaths []string
)

func downloadM3UFile(url, localFilePath string) error {
	fmt.Printf("Downloading M3U file: %s\n", url)

	// Set the custom User-Agent header
	userAgent := "IPTV Smarters/1.0.3 (iPad; iOS 16.6.1; Scale/2.00)"

	// Create a new HTTP client with a custom User-Agent header
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Follow redirects while preserving the custom User-Agent header
			req.Header.Set("User-Agent", userAgent)
			return nil
		},
	}

	// Create the GET request
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("error creating GET request: %v", err)
	}

	req.Header.Set("User-Agent", userAgent)

	// Perform the HTTP request
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("error fetching M3U file: %v", err)
	}
	defer resp.Body.Close()

	// Check for unexpected status code
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// Create or truncate the local file
	file, err := os.Create(localFilePath)
	if err != nil {
		return fmt.Errorf("error creating local file: %v", err)
	}
	defer file.Close()

	// Copy the content from the HTTP response to the local file
	_, err = io.Copy(file, resp.Body)
	if err != nil {
		// Check for unexpected EOF error
		if err == io.ErrUnexpectedEOF {
			return fmt.Errorf("unexpected EOF error: %v", err)
		}
		return fmt.Errorf("error copying content to local file: %v", err)
	}

	fmt.Printf("M3U file downloaded successfully to: %s\n", localFilePath)
	return nil
}

func deleteExistingM3UFiles(dataPath string) error {
	err := os.RemoveAll(dataPath)
	if err != nil {
		return fmt.Errorf("RemoveAll error: %v", err)
	}

	err = os.MkdirAll(dataPath, os.ModePerm)
	if err != nil {
		return fmt.Errorf("MkdirAll error: %v", err)
	}

	return nil
}

func loadM3UFiles(skipClearing bool) error {
	if !skipClearing {
		// Clear m3uURLs
		m3uFilePaths = []string{}
	}

	index := 1
	for {
		m3uUrl, m3uExists := os.LookupEnv(fmt.Sprintf("M3U_URL_%d", index))
		if !m3uExists {
			break
		}

		localM3uPath := filepath.Join(".", "data", fmt.Sprintf("m3u_%d.m3u", index))
		err := downloadM3UFile(m3uUrl, localM3uPath)
		if err != nil {
			return fmt.Errorf("downloadM3UFile error: %v", err)
		}

		m3uFilePaths = append(m3uFilePaths, localM3uPath)

		index++
	}

	return nil
}
