package projmux_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/crevissepartners/projmux/internal/services/agentsessions"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// Include the complete nested JSON graph, even fields absent from a read fixture.
func TestSessionHistoryPublicFieldCoverage(t *testing.T) {
	for _, pair := range [][2]reflect.Type{
		{reflect.TypeFor[agentsessions.AgentResult](), reflect.TypeFor[core.AgentSessionsList]()},
		{reflect.TypeFor[agentsessions.ProjectResult](), reflect.TypeFor[core.ProjectSessionsList]()},
	} {
		compareResourceShape(t, pair[0], pair[1], map[[2]reflect.Type]bool{})
		internal := reflect.New(pair[0]).Elem()
		public := reflect.New(pair[1]).Elem()
		fillResourceFields(internal)
		fillResourceFields(public)
		want, err := json.Marshal(internal.Interface())
		if err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(public.Interface())
		if err != nil || !bytes.Equal(want, got) {
			t.Fatalf("%s populated JSON differs: %s vs %s (%v)", pair[0], want, got, err)
		}
		want, err = json.Marshal(reflect.New(pair[0]).Interface())
		if err != nil {
			t.Fatal(err)
		}
		got, err = json.Marshal(reflect.New(pair[1]).Interface())
		if err != nil || !bytes.Equal(want, got) {
			t.Fatalf("%s zero JSON differs: %s vs %s (%v)", pair[0], want, got, err)
		}
	}
}
