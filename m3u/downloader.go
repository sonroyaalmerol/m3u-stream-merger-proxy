package m3u

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	"m3u-stream-merger/utils"
)

func downloadM3UToBuffer(m3uURL string, buffer *bytes.Buffer) (err error) {
	debug := os.Getenv("DEBUG") == "true"
	if debug {
		utils.SafeLogf("[DEBUG] Downloading M3U from: %s\n", m3uURL)
	}

	var file io.Reader

	if strings.HasPrefix(m3uURL, "file://") {
		localPath := strings.TrimPrefix(m3uURL, "file://")
		utils.SafeLogf("Reading M3U from local file: %s\n", localPath)

		localFile, err := os.Open(localPath)
		if err != nil {
			return fmt.Errorf("Error opening file: %v", err)
		}
		defer localFile.Close()

		file = localFile
	} else {
		utils.SafeLogf("Downloading M3U from URL: %s\n", m3uURL)
		resp, err := utils.CustomHttpRequest("GET", m3uURL)
		if err != nil {
			return fmt.Errorf("HTTP GET error: %v", err)
		}

		defer func() {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}()

		file = resp.Body
	}

	_, err = io.Copy(buffer, file)
	if err != nil {
		return fmt.Errorf("Error reading file: %v", err)
	}

	if debug {
		utils.SafeLogln("[DEBUG] Successfully copied M3U content to buffer")
	}

	return nil
}
