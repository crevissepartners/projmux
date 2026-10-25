package projmux_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// The public graph mirrors the complete internal JSON field graph, so a new
// field in any reachable internal struct fails until the public model follows.
func TestPublicResourceGraphFieldCoverage(t *testing.T) {
	for _, pair := range [][2]reflect.Type{
		{reflect.TypeFor[resourcegraph.Graph](), reflect.TypeFor[core.ResourceGraphView]()},
		{reflect.TypeFor[metadata.ControlSession](), reflect.TypeFor[core.ControlSession]()},
		{reflect.TypeFor[store.RegistryFileIdentity](), reflect.TypeFor[core.RegistryFileIdentity]()},
	} {
		compareResourceShape(t, pair[0], pair[1], map[[2]reflect.Type]bool{})
		source := reflect.New(pair[0]).Elem()
		fillResourceFields(source)
		b, err := json.Marshal(source.Interface())
		if err != nil {
			t.Fatal(err)
		}
		target := reflect.New(pair[1])
		if err = json.Unmarshal(b, target.Interface()); err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(target.Interface())
		if err != nil || !bytes.Equal(got, b) {
			t.Fatalf("%s full fixture JSON differs:\n%s\n%s\n%v", pair[0], b, got, err)
		}
		b, _ = json.Marshal(reflect.New(pair[0]).Interface())
		got, _ = json.Marshal(reflect.New(pair[1]).Interface())
		if !bytes.Equal(b, got) {
			t.Fatalf("%s zero JSON differs: %s vs %s", pair[0], b, got)
		}
	}
}
