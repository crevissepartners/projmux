package processhost

import (
	"errors"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

type inventoryObserver struct {
	snapshot Snapshot
	err      error
}

func (o inventoryObserver) Observe(Binding) (Snapshot, error) { return o.snapshot, o.err }

func TestInventoryObservationsRetainUnavailableDeclarations(t *testing.T) {
	b := Binding{Host: "host", Pane: "pane", Generation: "generation"}
	for _, tc := range []struct {
		name     string
		snapshot Snapshot
		err      error
		want     resourcegraph.Status
	}{
		{"ready", Snapshot{Binding: b, State: "ready"}, nil, resourcegraph.StatusLive},
		{"owned-starting", Snapshot{Binding: b, State: "starting", PID: 123}, nil, resourcegraph.StatusLive},
		{"starting-without-child", Snapshot{Binding: b, State: "starting"}, nil, resourcegraph.StatusUnknown},
		{"starting-with-exit", Snapshot{Binding: b, State: "starting", PID: 123, Exit: &Exit{Code: 0}}, nil, resourcegraph.StatusUnknown},
		{"actual-Wait", Snapshot{Binding: b, State: "exited", Exit: &Exit{Code: 0}}, nil, resourcegraph.StatusOffline},
		{"unavailable", Snapshot{}, errors.New("unavailable"), resourcegraph.StatusUnknown},
		{"contradictory", Snapshot{Binding: b, State: "ready", Exit: &Exit{Code: 0}}, nil, resourcegraph.StatusUnknown},
		{"EOF-without-Wait", Snapshot{Binding: b, State: "exited"}, nil, resourcegraph.StatusUnknown},
		{"foreign", Snapshot{Binding: Binding{Host: "other"}, State: "ready"}, nil, resourcegraph.StatusUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ObserveInventory([]InventoryTarget{{Binding: b, Observer: inventoryObserver{tc.snapshot, tc.err}}})
			if len(got.Declared) != 1 || got.Declared[0] != (resourcegraph.ProcessKey{Host: b.Host, Pane: b.Pane, Generation: b.Generation}) || len(got.Observed) != 1 || got.Observed[0].Status != tc.want {
				t.Fatalf("inventory: %+v", got)
			}
		})
	}
}
