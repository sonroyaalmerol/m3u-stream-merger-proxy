package sourceproc

import (
	"encoding/hex"
	"regexp"
	"time"

	"golang.org/x/crypto/sha3"
)

var (
	// attributeRegex matches M3U attributes in the format key="value"
	attributeRegex = regexp.MustCompile(`([a-zA-Z0-9_-]+)="([^"]*)"`)
)

// GenerateSessionID creates a unique session identifier
func GenerateSessionID() string {
	timestamp := time.Now().UnixNano()
	randomBytes := make([]byte, 16)
	for i := 0; i < 16; i++ {
		randomBytes[i] = byte(timestamp >> uint(i*8))
	}
	sessionHash := sha3.Sum224(randomBytes)
	return hex.EncodeToString(sessionHash[:])
}
