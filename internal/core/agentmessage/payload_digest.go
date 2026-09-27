package agentmessage

import (
	"crypto/sha256"
	"encoding/hex"
)

// PayloadSHA256 is the digest a reclaim history line carries for its
// envelope's payload: SHA-256 of the payload's exact bytes, as 64 lowercase
// hex characters. A consumer that holds the payload text elsewhere compares it
// with this same function rather than a copy of the rule.
func PayloadSHA256(payload string) string {
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}
