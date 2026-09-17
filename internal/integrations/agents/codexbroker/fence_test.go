package codexbroker

import (
	"strings"
	"testing"
)

func TestGenerationEndpointKeyRoundTripTable(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		domain     string
		generation string
		want       Refusal
	}{
		{name: "opaque", domain: "state-domain-a", generation: "generation-152-0", want: RefusalNone},
		{name: "canonical punctuation", domain: "domain:one_two", generation: "generation:two.one", want: RefusalNone},
		{name: "slash", domain: "domain/foreign", generation: "generation", want: RefusalEndpointIdentityInvalid},
		{name: "missing domain", generation: "generation", want: RefusalEndpointIdentityInvalid},
		{name: "missing generation", domain: "domain", want: RefusalEndpointIdentityInvalid},
		{name: "newline", domain: "domain\nforeign", generation: "generation", want: RefusalEndpointIdentityInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			key, err := NewEndpointKey(test.domain, test.generation)
			if got := RefusalOf(err); got != test.want {
				t.Fatalf("refusal = %s, want %s", got, test.want)
			}
			if test.want != RefusalNone {
				return
			}
			identity, ok := key.Identity()
			if !ok || identity != (EndpointIdentity{StateDomainID: test.domain, EndpointGenerationID: test.generation}) {
				t.Fatalf("identity = %+v, %v", identity, ok)
			}
			if key == DefaultEndpointKey {
				t.Fatal("a private generation collapsed onto the unmanaged default endpoint")
			}
		})
	}
	canonical, err := NewEndpointKey("f", "generation")
	if err != nil {
		t.Fatal(err)
	}
	// Raw base64 accepts non-zero trailing bits for some spellings. The key
	// must still reject a second encoding of the same durable identity.
	nonCanonical := EndpointKey(strings.Replace(string(canonical), "Zg:", "Zh:", 1))
	if _, ok := nonCanonical.Identity(); ok {
		t.Fatalf("non-canonical generation key decoded: %q", nonCanonical)
	}
}
