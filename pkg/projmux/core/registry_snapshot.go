package core

import (
	"reflect"
	"time"
)

// RegistrySnapshot contains the stored public resources and envelope of one
// Registry read. Rows retain their stored order, including duplicate UIDs and
// Offline Agents. Each slice is non-nil on success and its complete object
// graph belongs to the caller. No runtime observation or resolved ownership is
// added; a Pane's runtime kind is its stored Spec.Runtime, read with
// EffectiveKind.
type RegistrySnapshot struct {
	APIVersion       string            `json:"apiVersion"`
	SchemaVersion    int               `json:"schemaVersion"`
	UpdatedAt        time.Time         `json:"updatedAt"`
	Projects         []Project         `json:"projects"`
	ControlSessions  []ControlSession  `json:"controlSessions"`
	Windows          []Window          `json:"windows"`
	Panes            []Pane            `json:"panes"`
	Agents           []Agent           `json:"agents"`
	NameReservations []NameReservation `json:"nameReservations"`
}

// NameReservation is one stored name claim. Scope is empty for a top-level
// name and otherwise the UID of the resource that scopes it.
type NameReservation struct {
	Scope string `json:"scope,omitempty"`
	Kind  Kind   `json:"kind"`
	Name  string `json:"name"`
	UID   string `json:"uid"`
}

// RegistrySnapshotRead is one snapshot with the stat identity of the Registry
// file it was read from. RegistryIdentity is set only when the stats taken
// immediately before and after that read succeed and agree; otherwise it is
// nil. Reused reports that Snapshot is the caller's previous value, returned
// without reading the file again.
type RegistrySnapshotRead struct {
	Snapshot         RegistrySnapshot
	RegistryIdentity *RegistryFileIdentity
	Reused           bool
}

// Clone returns a deep copy: every nested slice, map and pointer is
// allocated anew, and nil and empty values, row order and values are kept
// exactly. It reads no file and normalizes nothing.
func (s RegistrySnapshot) Clone() RegistrySnapshot {
	var out RegistrySnapshot
	deepCopyValue(reflect.ValueOf(&out).Elem(), reflect.ValueOf(s))
	return out
}

// deepCopyValue copies src into the settable dst. Struct values are first
// copied whole, so values without exported fields (time.Time) keep their
// immutable internals; exported fields are then copied recursively.
func deepCopyValue(dst, src reflect.Value) {
	switch src.Kind() {
	case reflect.Pointer:
		if src.IsNil() {
			dst.SetZero()
			return
		}
		value := reflect.New(src.Type().Elem())
		deepCopyValue(value.Elem(), src.Elem())
		dst.Set(value)
	case reflect.Slice:
		if src.IsNil() {
			dst.SetZero()
			return
		}
		value := reflect.MakeSlice(src.Type(), src.Len(), src.Len())
		for i := range src.Len() {
			deepCopyValue(value.Index(i), src.Index(i))
		}
		dst.Set(value)
	case reflect.Map:
		if src.IsNil() {
			dst.SetZero()
			return
		}
		value := reflect.MakeMapWithSize(src.Type(), src.Len())
		for iter := src.MapRange(); iter.Next(); {
			key := reflect.New(src.Type().Key()).Elem()
			deepCopyValue(key, iter.Key())
			element := reflect.New(src.Type().Elem()).Elem()
			deepCopyValue(element, iter.Value())
			value.SetMapIndex(key, element)
		}
		dst.Set(value)
	case reflect.Interface:
		if src.IsNil() {
			dst.SetZero()
			return
		}
		value := reflect.New(src.Elem().Type()).Elem()
		deepCopyValue(value, src.Elem())
		dst.Set(value)
	case reflect.Struct:
		dst.Set(src)
		for i := range src.NumField() {
			if dst.Field(i).CanSet() {
				deepCopyValue(dst.Field(i), src.Field(i))
			}
		}
	case reflect.Array:
		for i := range src.Len() {
			deepCopyValue(dst.Index(i), src.Index(i))
		}
	default:
		dst.Set(src)
	}
}
