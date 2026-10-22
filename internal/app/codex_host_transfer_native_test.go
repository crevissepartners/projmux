package app

import (
	"errors"
	"reflect"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

func TestCodexTransferManagedExpectationPreservesConfirmedRecipeUntilAccepted(t *testing.T) {
	old := &coremetadata.Agent{}
	old.Metadata.UID = "agent-fixture"
	old.Metadata.Annotations = map[string]string{
		mutationruntime.CodexManagedBaseDigest: "confirmed-A",
		mutationruntime.CodexManagedBaseStatus: "base-delivered",
		"external/key":                         "old",
	}
	before := old.Clone()
	managed := &mutationruntime.ManagedCodexBase{Digest: "requested-B", Instructions: &codexappserver.ManagedInstructions{Accepted: true, Shared: true}}
	expected, err := managedCodexTransferExpectation(old, managed)
	if err != nil {
		t.Fatal(err)
	}
	if expected.Metadata.Annotations[mutationruntime.CodexManagedBaseStatus] != "base-unconfirmed" || expected.Metadata.Annotations[mutationruntime.CodexManagedBaseDigest] != "confirmed-A" || expected.Metadata.Annotations["external/key"] != "old" {
		t.Fatal("projection lost the confirmed recipe or adopted an unconfirmed digest")
	}
	if !reflect.DeepEqual(*old, before) {
		t.Fatal("projection modified the journal before validation")
	}
	managed.Instructions.Accepted = false
	if _, err := managedCodexTransferExpectation(old, managed); !errors.Is(err, processhost.ErrStale) {
		t.Fatalf("unconfirmed effect was projected: %v", err)
	}
}
