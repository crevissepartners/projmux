package projmux_test

import (
	"bytes"
	"encoding/json"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/profile"
	"github.com/crevissepartners/projmux/internal/core/runtimediag"
	"github.com/crevissepartners/projmux/internal/services/agentcatalog"
	"github.com/crevissepartners/projmux/internal/services/profiles"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
	"reflect"
	"testing"
	"time"
)

// Check the full JSON field graph, not just the currently populated fixture.
// New fields in any reachable internal struct fail until the public model follows.
func TestPublicResourceFieldCoverage(t *testing.T) {
	for _, pair := range [][2]reflect.Type{{reflect.TypeFor[profiles.WriteResult](), reflect.TypeFor[core.ProfileWriteResult]()}, {reflect.TypeFor[profiles.Info](), reflect.TypeFor[core.ProfileInfo]()}, {reflect.TypeFor[profile.Profile](), reflect.TypeFor[core.ProfileContent]()}, {reflect.TypeFor[metadata.Project](), reflect.TypeFor[core.Project]()}, {reflect.TypeFor[metadata.Window](), reflect.TypeFor[core.Window]()}, {reflect.TypeFor[metadata.Pane](), reflect.TypeFor[core.Pane]()}, {reflect.TypeFor[metadata.Agent](), reflect.TypeFor[core.Agent]()}, {reflect.TypeFor[runtimediag.Report](), reflect.TypeFor[core.RuntimeReport]()}, {reflect.TypeFor[agentcatalog.Capabilities](), reflect.TypeFor[core.Capabilities]()}, {reflect.TypeFor[agentcatalog.ModelSuggestions](), reflect.TypeFor[core.ModelSuggestions]()}} {
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
		// Omission behavior, including custom IsZero methods, must agree too.
		b, _ = json.Marshal(reflect.New(pair[0]).Interface())
		got, _ = json.Marshal(reflect.New(pair[1]).Interface())
		if !bytes.Equal(b, got) {
			t.Fatalf("%s zero JSON differs: %s vs %s", pair[0], b, got)
		}
	}
}
func compareResourceShape(t *testing.T, a, b reflect.Type, seen map[[2]reflect.Type]bool) {
	t.Helper()
	key := [2]reflect.Type{a, b}
	if seen[key] {
		return
	}
	seen[key] = true
	if a.Kind() != b.Kind() {
		t.Fatalf("shape %s vs %s", a, b)
	}
	if a.PkgPath() == "time" {
		if a != b {
			t.Fatal("time type differs")
		}
		return
	}
	switch a.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		compareResourceShape(t, a.Elem(), b.Elem(), seen)
	case reflect.Map:
		compareResourceShape(t, a.Key(), b.Key(), seen)
		compareResourceShape(t, a.Elem(), b.Elem(), seen)
	case reflect.Struct:
		n := 0
		for i := 0; i < a.NumField(); i++ {
			f := a.Field(i)
			if !f.IsExported() {
				continue
			}
			n++
			g, ok := b.FieldByName(f.Name)
			if !ok || f.Tag.Get("json") != g.Tag.Get("json") {
				t.Fatalf("missing/different JSON field %s.%s", a, f.Name)
			}
			compareResourceShape(t, f.Type, g.Type, seen)
		}
		if n != b.NumField() {
			t.Fatalf("extra public fields %s", b)
		}
	}
}
func fillResourceFields(v reflect.Value) {
	if v.Type() == reflect.TypeFor[time.Time]() {
		v.Set(reflect.ValueOf(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)))
		return
	}
	switch v.Kind() {
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fillResourceFields(v.Elem())
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Field(i).CanSet() {
				fillResourceFields(v.Field(i))
			}
		}
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fillResourceFields(v.Index(0))
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
		k := reflect.New(v.Type().Key()).Elem()
		x := reflect.New(v.Type().Elem()).Elem()
		fillResourceFields(k)
		fillResourceFields(x)
		v.SetMapIndex(k, x)
	case reflect.String:
		v.SetString("populated")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int64, reflect.Int32:
		v.SetInt(7)
	case reflect.Uint, reflect.Uint64, reflect.Uint32:
		v.SetUint(7)
	}
}
