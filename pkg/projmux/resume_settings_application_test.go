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

// A prepare reply carries the settings application on the wire and the SDK
// copies it; a reply from an owner built before the field decodes to nil.
func TestPrepareReplyCarriesSettingsApplication(t *testing.T) {
	want := &mutationruntime.AgentSettingsApplication{NotAppliedCount: 1, Entries: []mutationruntime.AgentSettingApplication{{Setting: "model", Status: "pending", Reason: "first-input-required"}}}
	raw, err := json.Marshal(mutationruntime.ProcessDetachedReply{Phase: "prepared", SettingsApplication: want})
	if err != nil {
		t.Fatal(err)
	}
	var reply mutationruntime.ProcessDetachedReply
	if err = json.Unmarshal(raw, &reply); err != nil {
		t.Fatal(err)
	}
	got := publicSettingsApplication(reply.SettingsApplication)
	if got == nil || got.NotAppliedCount != 1 || len(got.Entries) != 1 || got.Entries[0].Setting != "model" || got.Entries[0].Status != "pending" || got.Entries[0].Reason != "first-input-required" {
		t.Fatalf("public application %+v from wire %s", got, raw)
	}
	old := []byte(`{"Phase":"prepared","Preparation":{"AgentUID":"a"}}`)
	var legacy mutationruntime.ProcessDetachedReply
	if err = json.Unmarshal(old, &legacy); err != nil || publicSettingsApplication(legacy.SettingsApplication) != nil {
		t.Fatalf("owner without the field decoded %+v %v", legacy.SettingsApplication, err)
	}
}
