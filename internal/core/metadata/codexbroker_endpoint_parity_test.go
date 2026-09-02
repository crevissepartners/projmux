package metadata_test

import (
	"testing"

	"github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
)

// TestBrokerEndpointIdentityValidityMatchesDurableMetadataRef prevents the
// transport pool from growing a second endpoint-ID vocabulary. The broker
// cannot import core metadata by package contract, so this reverse boundary
// test pins the duplicate validator byte-for-byte.
func TestBrokerEndpointIdentityValidityMatchesDurableMetadataRef(t *testing.T) {
	t.Parallel()
	values := []string{"domain", "DOMAIN_1", "domain.one:two", "", " domain", "domain/foreign", "domain foreign", "domain\nforeign"}
	for _, domain := range values {
		for _, generation := range values {
			brokerValid := (codexbroker.EndpointIdentity{StateDomainID: domain, EndpointGenerationID: generation}).Valid()
			metadataValid := (metadata.CodexEndpointRef{StateDomainID: domain, EndpointGenerationID: generation}).Valid()
			if brokerValid != metadataValid {
				t.Fatalf("validity drift for %q/%q: broker=%v metadata=%v", domain, generation, brokerValid, metadataValid)
			}
		}
	}
}
