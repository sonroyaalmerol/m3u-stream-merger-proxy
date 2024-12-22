package store

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"m3u-stream-merger/utils"
)

func DownloadM3USource(m3uIndex string) (err error) {
	debug := os.Getenv("DEBUG") == "true"
	m3uURL := os.Getenv(fmt.Sprintf("M3U_URL_%s", m3uIndex))

	if debug {
		utils.SafeLogf("[DEBUG] Processing M3U from: %s\n", m3uURL)
	}

	finalPath := utils.GetM3UFilePathByIndex(m3uIndex)
	tmpPath := finalPath + ".new"

	// Handle local file URLs
	if strings.HasPrefix(m3uURL, "file://") {
		localPath := strings.TrimPrefix(m3uURL, "file://")
		if debug {
			utils.SafeLogf("[DEBUG] Local M3U file detected: %s\n", localPath)
		}

		// Ensure finalPath's directory exists
		err := os.MkdirAll(filepath.Dir(finalPath), os.ModePerm)
		if err != nil {
			return fmt.Errorf("Error creating directories for final path: %v", err)
		}

		_ = os.Remove(finalPath)

		// Create a symlink
		err = os.Symlink(localPath, finalPath)
		if err != nil {
			return fmt.Errorf("Error creating symlink: %v", err)
		}

		if debug {
			utils.SafeLogf("[DEBUG] Symlink created from %s to %s\n", localPath, finalPath)
		}

		return nil
	}

	// Handle remote URLs
	if debug {
		utils.SafeLogf("[DEBUG] Remote M3U URL detected: %s\n", m3uURL)
	}

	resp, err := utils.CustomHttpRequest("GET", m3uURL)
	if err != nil {
		return fmt.Errorf("HTTP GET error: %v", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body) // Discard remaining body content
		resp.Body.Close()
	}()

	// Ensure finalPath's directory exists
	err = os.MkdirAll(filepath.Dir(finalPath), os.ModePerm)
	if err != nil {
		return fmt.Errorf("Error creating directories for final path: %v", err)
	}

	// Write response body to finalPath
	outFile, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("Error creating file: %v", err)
	}
	defer outFile.Close()

	_, err = io.Copy(outFile, resp.Body)
	if err != nil {
		return fmt.Errorf("Error writing to file: %v", err)
	}

	_ = os.Remove(finalPath)
	_ = os.Rename(tmpPath, finalPath)

	if debug {
		utils.SafeLogf("[DEBUG] M3U file downloaded to %s\n", finalPath)
	}

	return nil
}
