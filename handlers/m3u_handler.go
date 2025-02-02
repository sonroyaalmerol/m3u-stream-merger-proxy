package handlers

import (
	"m3u-stream-merger/logger"
	"m3u-stream-merger/store"
	"net/http"
	"os"
	"strings"
	"time"
)

type M3UHandler struct {
	logger logger.Logger
	Cache  *store.Cache
}

func NewM3UHandler(logger logger.Logger) *M3UHandler {
	return &M3UHandler{
		logger: logger,
		Cache:  store.M3uCache,
	}
}

func (h *M3UHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
					h.logger.Warnf("invalid credential format: %s\n", arrItem)
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
		pass := r.URL.Query().Get("password")
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

	if _, err := w.Write([]byte(content)); err != nil {
		h.logger.Debugf("Error writing http response: %v\n", err)
	}
}
