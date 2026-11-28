package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
)

const keyPrefix = "dsp_live_"

// GenerateKey returns a new API key. The raw key is shown to the tenant once;
// only the hash is stored. Keys carry 256 bits of entropy, so a plain
// SHA-256 is the right hash
func GenerateKey() (raw, displayPrefix string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", nil, fmt.Errorf("generate api key: %w", err)
	}

	raw = keyPrefix + base64.RawURLEncoding.EncodeToString(b)
	return raw, raw[:len(keyPrefix)+4], HashKey(raw), nil
}

func HashKey(raw string) []byte {
	h := sha256.Sum256([]byte(raw))
	return h[:]
}

// BearerToken extracts the token from "Authorization: Bearer <token>".
func BearerToken(header string) (string, bool) {
	scheme, token, ok := strings.Cut(strings.TrimSpace(header), " ")
	token = strings.TrimSpace(token)
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return "", false
	}

	return token, true
}
