package metadata

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"strings"
)

// uidEntropyBytes is the random payload size behind every uid.
const uidEntropyBytes = 16

var uidEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// uidPrefixes keep a uid self-describing in logs and tmux options without
// making it derivable from any resource field.
var uidPrefixes = map[Kind]string{
	KindProject: "proj",
	KindWindow:  "win",
	KindPane:    "pane",
	KindAgent:   "agent",
}

// NewUID mints an opaque Projmux identity for kind. The value is independent
// of tmux lifecycle, of the resource's name, and of its root path, so it
// survives snapshot/restore and rebind unchanged.
//
// Entropy comes from crypto/rand; callers that need determinism inject their
// own generator through Mutator.NewUID.
func NewUID(kind Kind) (string, error) {
	prefix, ok := uidPrefixes[kind]
	if !ok {
		return "", stateErr("uid", ErrInvalidRegistry, "unsupported resource kind %q", kind)
	}
	buf := make([]byte, uidEntropyBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("metadata: read uid entropy: %w", err)
	}
	return prefix + "-" + strings.ToLower(uidEncoding.EncodeToString(buf)), nil
}

// UIDKind reports the kind encoded in a uid prefix. It is a debugging aid;
// ownership and lookups always go through the registry, never through the
// prefix.
func UIDKind(uid string) (Kind, bool) {
	prefix, _, ok := strings.Cut(uid, "-")
	if !ok {
		return "", false
	}
	for kind, candidate := range uidPrefixes {
		if candidate == prefix {
			return kind, true
		}
	}
	return "", false
}
