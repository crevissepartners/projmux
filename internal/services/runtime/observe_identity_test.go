package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	inventory "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

func TestReaderResolveIdentifiedStatsAroundOneLoad(t *testing.T) {
	first := inventory.RegistryFileIdentity{Inode: 1, Size: 2}
	second := inventory.RegistryFileIdentity{Inode: 3, Size: 2}
	errStat := errors.New("stat failed")
	for _, tc := range []struct {
		name  string
		stats []inventory.RegistryFileIdentity
		errs  []error
		want  *inventory.RegistryFileIdentity
	}{
		{name: "unchanged", stats: []inventory.RegistryFileIdentity{first, first}, errs: []error{nil, nil}, want: &first},
		{name: "replaced", stats: []inventory.RegistryFileIdentity{first, second}, errs: []error{nil, nil}},
		{name: "before failed", stats: []inventory.RegistryFileIdentity{{}, first}, errs: []error{errStat, nil}},
		{name: "after failed", stats: []inventory.RegistryFileIdentity{first, {}}, errs: []error{nil, errStat}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var order []string
			reader := Reader{
				LoadRegistry: func() (metadata.Registry, error) {
					order = append(order, "load")
					return metadata.Registry{}, nil
				},
				Identity: func() (inventory.RegistryFileIdentity, error) {
					i := 0
					for _, step := range order {
						if step == "stat" {
							i++
						}
					}
					order = append(order, "stat")
					return tc.stats[i], tc.errs[i]
				},
				Observe: func(context.Context, resourcegraph.Transport) resourcegraph.Inventory {
					order = append(order, "observe")
					return resourcegraph.Inventory{}
				},
				Processes: func(context.Context, metadata.Registry) resourcegraph.ProcessInventory {
					order = append(order, "processes")
					return resourcegraph.ProcessInventory{}
				},
			}
			_, identity, err := reader.ResolveIdentified(context.Background(), resourcegraph.Transport{Kind: resourcegraph.TransportNone})
			if err != nil {
				t.Fatal(err)
			}
			// A failed first stat already rules the identity out.
			want := "stat load stat observe processes"
			if tc.errs[0] != nil {
				want = "stat load observe processes"
			}
			if got := strings.Join(order, " "); got != want {
				t.Fatalf("order %q want %q", got, want)
			}
			if (identity == nil) != (tc.want == nil) || identity != nil && *identity != *tc.want {
				t.Fatalf("identity %+v want %+v", identity, tc.want)
			}
		})
	}
}

func TestReaderResolveDoesNotStat(t *testing.T) {
	reader := Reader{
		LoadRegistry: func() (metadata.Registry, error) { return metadata.Registry{}, nil },
		Identity: func() (inventory.RegistryFileIdentity, error) {
			t.Fatal("Resolve stat the Registry file")
			return inventory.RegistryFileIdentity{}, nil
		},
		Observe: func(context.Context, resourcegraph.Transport) resourcegraph.Inventory {
			return resourcegraph.Inventory{}
		},
	}
	if _, err := reader.Resolve(context.Background(), resourcegraph.Transport{Kind: resourcegraph.TransportNone}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (Reader{}).ResolveIdentified(context.Background(), resourcegraph.Transport{}); err == nil {
		t.Fatal("unconfigured identity accepted")
	}
}
