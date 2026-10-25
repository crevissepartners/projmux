package projmux_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/agentsettings"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// The public launch settings mirror the internal settings JSON field graph, so
// a new internal field fails until the public model follows.
func TestPublicAgentLaunchSettingsFieldCoverage(t *testing.T) {
	internal, public := reflect.TypeFor[agentsettings.Settings](), reflect.TypeFor[core.AgentLaunchSettings]()
	compareResourceShape(t, internal, public, map[[2]reflect.Type]bool{})
	source := reflect.New(internal).Elem()
	fillResourceFields(source)
	want, err := json.Marshal(source.Interface())
	if err != nil {
		t.Fatal(err)
	}
	var decoded core.AgentLaunchSettings
	if err := json.Unmarshal(want, &decoded); err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(decoded)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("full fixture JSON differs:\n%s\n%s\n%v", want, got, err)
	}
	want, _ = json.Marshal(agentsettings.Settings{})
	got, _ = json.Marshal(core.AgentLaunchSettings{})
	if !bytes.Equal(got, want) {
		t.Fatalf("zero JSON differs: %s vs %s", want, got)
	}
}
