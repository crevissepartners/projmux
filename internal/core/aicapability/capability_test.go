package aicapability

import (
	"errors"
	"testing"
)

func TestCacheInvalidatesSelectionsAcrossConnectionAndVersionEpochs(t *testing.T) {
	t.Parallel()
	cache := &Cache{}
	first := Snapshot{Epoch: Epoch{Connection: "connection-1", Version: "0.149.0"}, Models: []Model{{
		ID: "model-a", LaunchName: "model-a", Efforts: []string{"low", "high"},
	}}}
	cache.Replace(first)
	selection := Selection{Epoch: first.Epoch, ModelID: "model-a", LaunchName: "model-a", Effort: "high"}
	if _, err := cache.Validate(selection); err != nil {
		t.Fatalf("current selection rejected: %v", err)
	}

	cache.Replace(Snapshot{Epoch: Epoch{Connection: "connection-2", Version: "0.150.0"}, Models: []Model{{
		ID: "model-a", LaunchName: "model-a", Efforts: []string{"low"},
	}}})
	if _, err := cache.Validate(selection); !errors.Is(err, ErrStaleSelection) {
		t.Fatalf("old epoch selection error = %v, want ErrStaleSelection", err)
	}

	current := Selection{Epoch: Epoch{Connection: "connection-2", Version: "0.150.0"}, ModelID: "model-a", LaunchName: "model-a", Effort: "high"}
	if _, err := cache.Validate(current); !errors.Is(err, ErrStaleSelection) {
		t.Fatalf("removed effort selection error = %v, want ErrStaleSelection", err)
	}
}

func TestSnapshotCloneDoesNotAliasCapabilities(t *testing.T) {
	t.Parallel()
	snapshot := Snapshot{Epoch: Epoch{Connection: "connection-1", Version: "0.149.0"}, Models: []Model{{
		ID: "model-a", Efforts: []string{"medium"}, InputModalities: []string{"text"}, SupportsPersonality: true,
	}}}
	clone := snapshot.Clone()
	clone.Models[0].Efforts[0] = "tampered"
	clone.Models[0].InputModalities[0] = "tampered"
	if snapshot.Models[0].Efforts[0] != "medium" || snapshot.Models[0].InputModalities[0] != "text" || !snapshot.Models[0].SupportsPersonality {
		t.Fatalf("clone aliases source: %#v", snapshot.Models[0])
	}
}
