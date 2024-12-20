package utils

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
)

func GenerateFingerprint(r *http.Request) string {
	// Collect relevant attributes
	ip := r.RemoteAddr
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		ip = xff
	}
	userAgent := r.Header.Get("User-Agent")
	accept := r.Header.Get("Accept")
	acceptLang := r.Header.Get("Accept-Language")

	// Combine into a single string
	data := fmt.Sprintf("%s|%s|%s|%s", ip, userAgent, accept, acceptLang)

	// Hash the string for a compact, fixed-length identifier
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}
