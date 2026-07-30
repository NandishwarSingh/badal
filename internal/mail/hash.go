package mail

import (
	"crypto/sha256"
	"encoding/hex"
)

// shortHash keeps archive keys stable and filesystem-safe. Message-IDs contain
// characters that have no business in an object key.
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:16]
}
