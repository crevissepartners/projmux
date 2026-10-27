package projmux_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// The snapshot mirrors the whole stored Registry envelope: every Registry JSON
// field has a snapshot field of the same name and shape. Collections are
// always present in the snapshot, so only the omitempty option may differ.
func TestRegistrySnapshotEnvelopeFieldCoverage(t *testing.T) {
	stored, public := reflect.TypeFor[metadata.Registry](), reflect.TypeFor[core.RegistrySnapshot]()
	if stored.NumField() != public.NumField() {
		t.Fatalf("field count %d vs %d", stored.NumField(), public.NumField())
	}
	for i := range stored.NumField() {
		f, g := stored.Field(i), public.Field(i)
		name := strings.TrimSuffix(f.Tag.Get("json"), ",omitempty")
		if f.Name != g.Name || name != g.Tag.Get("json") {
			t.Fatalf("field %d: %s %q vs %s %q", i, f.Name, f.Tag.Get("json"), g.Name, g.Tag.Get("json"))
		}
		compareResourceShape(t, f.Type, g.Type, map[[2]reflect.Type]bool{})
	}
	compareResourceShape(t, reflect.TypeFor[metadata.NameReservation](), reflect.TypeFor[core.NameReservation](), map[[2]reflect.Type]bool{})
	source := metadata.NameReservation{}
	fillResourceFields(reflect.ValueOf(&source).Elem())
	raw, _ := json.Marshal(source)
	var target core.NameReservation
	if err := json.Unmarshal(raw, &target); err != nil {
		t.Fatal(err)
	}
	if got, _ := json.Marshal(target); string(got) != string(raw) {
		t.Fatalf("NameReservation JSON differs: %s vs %s", got, raw)
	}
}

// Clone copies every value and allocates every nested slice, map and pointer
// anew; nil stays nil and empty stays empty.
func TestRegistrySnapshotCloneOwnership(t *testing.T) {
	var full core.RegistrySnapshot
	fillResourceFields(reflect.ValueOf(&full).Elem())
	clone := full.Clone()
	if !reflect.DeepEqual(clone, full) {
		t.Fatal("clone differs")
	}
	if n := assertNoSharedMemory(t, reflect.ValueOf(clone), reflect.ValueOf(full), "snapshot"); n == 0 {
		t.Fatal("fixture has no nested references")
	}
	before, _ := json.Marshal(full)
	clone.Projects[0].Metadata.Labels["populated"] = "changed"
	clone.Panes[0].Status.Conditions[0].Type = "changed"
	clone.Agents[0].Metadata.OwnerRef.UID = "changed"
	clone.NameReservations[0].Name = "changed"
	if after, _ := json.Marshal(full); string(after) != string(before) {
		t.Fatal("changing the clone changed the original")
	}

	// nil and empty values are kept apart at the top level and when nested.
	sparse := core.RegistrySnapshot{
		UpdatedAt:       time.Date(2026, 10, 27, 1, 2, 3, 4, time.FixedZone("KST", 9*60*60)),
		Projects:        []core.Project{},
		ControlSessions: nil,
		Windows:         []core.Window{{Metadata: core.ObjectMeta{Labels: map[string]string{}}}},
		Panes:           []core.Pane{{Status: core.PaneStatus{Conditions: []core.Condition{}}}},
		Agents:          []core.Agent{{}},
	}
	got := sparse.Clone()
	if !reflect.DeepEqual(got, sparse) || got.Projects == nil || got.ControlSessions != nil || got.NameReservations != nil ||
		got.Windows[0].Metadata.Labels == nil || got.Windows[0].Metadata.Annotations != nil ||
		got.Panes[0].Status.Conditions == nil || got.Agents[0].Metadata.OwnerRef != nil || !got.UpdatedAt.Equal(sparse.UpdatedAt) ||
		got.UpdatedAt.Location() != sparse.UpdatedAt.Location() {
		t.Fatalf("nil/empty not preserved: %+v", got)
	}
	if zero := (core.RegistrySnapshot{}).Clone(); !reflect.DeepEqual(zero, core.RegistrySnapshot{}) {
		t.Fatalf("zero clone: %+v", zero)
	}
}

// assertNoSharedMemory walks equal values and fails when a non-nil slice,
// map or pointer of a is the same memory as in b. It returns how many
// references it compared.
func assertNoSharedMemory(t *testing.T, a, b reflect.Value, path string) int {
	t.Helper()
	if a.Type() == reflect.TypeFor[time.Time]() {
		return 0
	}
	n := 0
	switch a.Kind() {
	case reflect.Pointer:
		if a.IsNil() {
			return 0
		}
		if a.Pointer() == b.Pointer() {
			t.Fatalf("%s shares a pointer", path)
		}
		return 1 + assertNoSharedMemory(t, a.Elem(), b.Elem(), path)
	case reflect.Slice:
		if a.IsNil() {
			return 0
		}
		if a.Len() > 0 && a.Pointer() == b.Pointer() {
			t.Fatalf("%s shares a slice", path)
		}
		n++
		for i := range a.Len() {
			n += assertNoSharedMemory(t, a.Index(i), b.Index(i), path+"[]")
		}
	case reflect.Map:
		if a.IsNil() {
			return 0
		}
		if a.Pointer() == b.Pointer() {
			t.Fatalf("%s shares a map", path)
		}
		n++
		for iter := a.MapRange(); iter.Next(); {
			n += assertNoSharedMemory(t, iter.Value(), b.MapIndex(iter.Key()), path+"{}")
		}
	case reflect.Struct:
		for i := range a.NumField() {
			if a.Type().Field(i).IsExported() {
				n += assertNoSharedMemory(t, a.Field(i), b.Field(i), path+"."+a.Type().Field(i).Name)
			}
		}
	}
	return n
}
