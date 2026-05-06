package notify

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
)

// idEntropyBytes is the byte width of the random portion of an auto-generated
// notification id. 10 bytes -> 16 base32 chars.
const idEntropyBytes = 10

// generateID produces a short crockford-style base32 id. We use the stdlib
// base32 encoder with its default alphabet (RFC 4648) and strip padding so
// the id stays compact and url-safe-enough for json.
func generateID() (string, error) {
	buf := make([]byte, idEntropyBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf), nil
}
