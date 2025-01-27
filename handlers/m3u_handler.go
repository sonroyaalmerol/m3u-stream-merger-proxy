package handlers

import (
	"m3u-stream-merger/store"
	"m3u-stream-merger/utils"
	"net/http"
	"os"
	"strings"
	"time"
)

func M3UHandler(w http.ResponseWriter, r *http.Request) {
	debug := os.Getenv("DEBUG") == "true"

	w.Header().Set("Access-Control-Allow-Origin", "*")

	credentials := os.Getenv("CREDENTIALS")
	var credentialsArr [][]string
	if credentials != "" && strings.ToLower(credentials) != "none" {
		arr := strings.Split(credentials, "|")
		for _, arrItem := range arr {
			cred := strings.Split(arrItem, ":")
			// if only username is set, set password to ""
			if len(cred) == 1 {
				cred = append(cred, "")
			} else if len(cred) == 3 {
				// date is also provided, check if we are before the provided date (expiration date)
				d, err := time.Parse(time.DateOnly, cred[3])
				if err != nil {
					utils.SafeLogf("[WARNING] invalid credential format: %s\n", arrItem)
					continue
				}
				if time.Now().After(d) {
					// Expired access
					continue
				}
			}
			credentialsArr = append(credentialsArr, cred)
		}
	}

	if len(credentialsArr) > 0 {
		// download requires authorization
		user := r.URL.Query().Get("username")
		pass := r.URL.Query().Get("username")
		if len(user) == 0 || len(pass) == 0 {
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}
		authCorrect := false
		for _, cred := range credentialsArr {
			if strings.EqualFold(user, cred[0]) && strings.EqualFold(pass, cred[1]) {
				authCorrect = true
				break
			}
		}
		if !authCorrect {
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}
	}

	w.Header().Set("Content-Type", "text/plain")

	content := store.RevalidatingGetM3U(r, false)
	_, err := w.Write([]byte(content))
	if err != nil {
		if debug {
			utils.SafeLogf("[DEBUG] Error writing http response: %v\n", err)
		}
	}
}
