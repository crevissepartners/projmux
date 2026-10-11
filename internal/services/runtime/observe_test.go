package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

func TestResolveCanceledBeforeDependencies(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	reader := Reader{LoadRegistry: func() (metadata.Registry, error) { called = true; return metadata.NewRegistry(), nil }, Observe: func(context.Context, resourcegraph.Transport) resourcegraph.Inventory {
		called = true
		return resourcegraph.Inventory{}
	}}
	_, err := reader.Resolve(ctx, resourcegraph.Transport{})
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("error=%v dependencies called=%v", err, called)
	}
}

func TestResolveCanceledAfterRegistryBeforeObservation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observed := false
	reader := Reader{LoadRegistry: func() (metadata.Registry, error) { cancel(); return metadata.NewRegistry(), nil }, Observe: func(context.Context, resourcegraph.Transport) resourcegraph.Inventory {
		observed = true
		return resourcegraph.Inventory{}
	}}
	_, err := reader.Resolve(ctx, resourcegraph.Transport{})
	if !errors.Is(err, context.Canceled) || observed {
		t.Fatalf("error=%v observed=%v", err, observed)
	}
}
