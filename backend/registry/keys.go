package registry

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// KeyPrefix starts every generated API key, so leaked keys are easy to
// recognise in logs and by secret scanners.
const KeyPrefix = "opk_"

// NewKey generates an API key. Store only HashKey(key); show the key once.
func NewKey() string {
	b := make([]byte, 32)
	rand.Read(b)
	return KeyPrefix + base64.RawURLEncoding.EncodeToString(b)
}

// HashKey is the stored form of a key.
func HashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}
