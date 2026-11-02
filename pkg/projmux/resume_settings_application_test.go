package projmux

import (
	"encoding/json"
	"reflect"
	"testing"

	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

// A detached owner's committed reply carries the settings application on the
// wire, and the SDK copies it entry by entry; a reply without it stays nil.
func TestResumeProcessCommittedCarriesSettingsApplication(t *testing.T) {
	model, requested := "sonnet", "haiku"
	want := &mutationruntime.AgentSettingsApplication{NotAppliedCount: 1, Entries: []mutationruntime.AgentSettingApplication{
		{Setting: "model", Status: "applied", Reason: "requested", Value: &model},
		{Setting: "effort", Status: "not-applied", Reason: "unsupported", RequestedValue: &requested, NextAction: "relaunch"},
	}}
	raw, err := json.Marshal(mutationruntime.ProcessCreateCommitted{SettingsApplication: want})
	if err != nil {
		t.Fatal(err)
	}
	var wire mutationruntime.ProcessCreateCommitted
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	got := publicSettingsApplication(wire.SettingsApplication)
	if got == nil || got.NotAppliedCount != want.NotAppliedCount || len(got.Entries) != len(want.Entries) {
		t.Fatalf("public application %+v from wire %s", got, raw)
	}
	for i, e := range want.Entries {
		g := got.Entries[i]
		if g.Setting != e.Setting || g.Status != e.Status || g.Reason != e.Reason || g.NextAction != e.NextAction || !reflect.DeepEqual(g.Value, e.Value) || !reflect.DeepEqual(g.RequestedValue, e.RequestedValue) {
			t.Fatalf("entry %d (setting %q) differs: public %+v wire %+v", i, e.Setting, g, e)
		}
	}
	empty, err := json.Marshal(mutationruntime.ProcessCreateCommitted{})
	if err != nil {
		t.Fatal(err)
	}
	var plain mutationruntime.ProcessCreateCommitted
	if err := json.Unmarshal(empty, &plain); err != nil || publicSettingsApplication(plain.SettingsApplication) != nil {
		t.Fatalf("absent application decoded %+v %v from %s", plain.SettingsApplication, err, empty)
	}
}
