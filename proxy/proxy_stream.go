package proxy

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"m3u-stream-merger/utils"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
)

func (instance *StreamInstance) ProxyStream(ctx context.Context, m3uIndex string, subIndex string, resp *http.Response, r *http.Request, w http.ResponseWriter, statusChan chan int) {
	debug := os.Getenv("DEBUG") == "true"

	if r.Method != http.MethodGet || utils.EOFIsExpected(resp) {
		scanner := bufio.NewScanner(resp.Body)
		base, err := url.Parse(resp.Request.URL.String())
		if err != nil {
			utils.SafeLogf("Invalid base URL for M3U8 stream: %v", err)
			statusChan <- 4
			return
		}

		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "#") {
				_, err := w.Write([]byte(line + "\n"))
				if err != nil {
					utils.SafeLogf("Failed to write line to response: %v", err)
					statusChan <- 4
					return
				}
			} else if strings.TrimSpace(line) != "" {
				u, err := url.Parse(line)
				if err != nil {
					utils.SafeLogf("Failed to parse M3U8 URL in line: %v", err)
					_, err := w.Write([]byte(line + "\n"))
					if err != nil {
						utils.SafeLogf("Failed to write line to response: %v", err)
						statusChan <- 4
						return
					}
					continue
				}

				if !u.IsAbs() {
					u = base.ResolveReference(u)
				}

				ext := ""
				baseSplit := strings.Split(path.Base(r.URL.Path), ".")
				if len(baseSplit) > 1 {
					ext = baseSplit[len(baseSplit)-1]
				}

				origUrl := u.String()
				encodedUrl := base64.URLEncoding.EncodeToString([]byte(origUrl))
				newUrl := fmt.Sprintf("%s://%s/v/%s", r.URL.Scheme, r.URL.Host, encodedUrl)

				if ext != "" {
					newUrl += "." + ext
				}

				_, err = w.Write([]byte(newUrl + "\n"))
				if err != nil {
					utils.SafeLogf("Failed to write URL to response: %v", err)
					statusChan <- 4
					return
				}
			}
		}

		statusChan <- 4
		return
	}

	instance.Cm.UpdateConcurrency(m3uIndex, true)
	defer func() {
		if debug {
			utils.SafeLogf("[DEBUG] Defer executed for stream: %s\n", r.RemoteAddr)
		}
		instance.Cm.UpdateConcurrency(m3uIndex, false)
	}()

	statusChan <- ProxyVideoStream(ctx, resp, r, w)
}
