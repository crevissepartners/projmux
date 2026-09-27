package agentmessage

import (
	"regexp"
	"testing"
)

// TestPayloadSHA256PinsTheKnownVector pins the digest to the FIPS 180-2 "abc"
// vector and to its 64-character lowercase hex form, so a history line written
// by one build compares equal in any other.
func TestPayloadSHA256PinsTheKnownVector(t *testing.T) {
	t.Parallel()
	if got, want := PayloadSHA256("abc"), "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"; got != want {
		t.Fatalf("PayloadSHA256(abc) = %s, want %s", got, want)
	}
	lowerHex := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, payload := range []string{"abc", "operator text for message-1", "한글 payload"} {
		if got := PayloadSHA256(payload); !lowerHex.MatchString(got) {
			t.Fatalf("PayloadSHA256(%q) = %q, want 64 lowercase hex characters", payload, got)
		}
	}
	if PayloadSHA256("abc") == PayloadSHA256("abc ") {
		t.Fatal("a trailing byte did not change the digest")
	}
}
