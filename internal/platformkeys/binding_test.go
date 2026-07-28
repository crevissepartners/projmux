package platformkeys

import (
	"reflect"
	"testing"
)

func TestParseBindingPortableChords(t *testing.T) {
	tests := []struct {
		chord     string
		keyCode   uint16
		modifiers Modifiers
	}{
		{chord: "M-1", keyCode: 18, modifiers: ModifierAlt},
		{chord: "M-a", keyCode: 0, modifiers: ModifierAlt},
		{chord: "M-A", keyCode: 0, modifiers: ModifierAlt | ModifierShift},
		{chord: "C-M-s", keyCode: 1, modifiers: ModifierControl | ModifierAlt},
		{chord: "M-S-Left", keyCode: 123, modifiers: ModifierAlt | ModifierShift},
		{chord: "C-F12", keyCode: 111, modifiers: ModifierControl},
	}
	for _, tt := range tests {
		t.Run(tt.chord, func(t *testing.T) {
			got, ok := ParseBinding(tt.chord)
			if !ok {
				t.Fatalf("ParseBinding(%q) unsupported", tt.chord)
			}
			want := Binding{Chord: tt.chord, KeyCode: tt.keyCode, Modifiers: tt.modifiers}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("ParseBinding(%q) = %#v, want %#v", tt.chord, got, want)
			}
		})
	}
}

func TestParseBindingLeavesOrdinaryTerminalInputAlone(t *testing.T) {
	for _, chord := range []string{"a", "Left", "S-a", "MouseDown1Status", "M-Unknown"} {
		if got, ok := ParseBinding(chord); ok {
			t.Fatalf("ParseBinding(%q) = %#v, want unsupported", chord, got)
		}
	}
}

func TestParseBindingsDeduplicatesPhysicalChord(t *testing.T) {
	got := ParseBindings([]string{"M-a", "M-a", "M-1", "plain"})
	want := []Binding{
		{Chord: "M-a", KeyCode: 0, Modifiers: ModifierAlt},
		{Chord: "M-1", KeyCode: 18, Modifiers: ModifierAlt},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseBindings() = %#v, want %#v", got, want)
	}
}

func TestCaptureBindingsIncludeCustomOptionAndControlChords(t *testing.T) {
	bindings := captureBindings()
	for _, chord := range []string{"M-a", "M-S-a", "M-1", "M-Left", "C-r", "C-M-F12"} {
		binding, ok := ParseBinding(chord)
		if !ok {
			t.Fatalf("test chord %q is unsupported", chord)
		}
		found := false
		for _, candidate := range bindings {
			if reflect.DeepEqual(candidate, binding) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("captureBindings() does not include %q", chord)
		}
	}
}
