//go:build darwin && cgo

package platformkeys

import (
	"reflect"
	"testing"
	"time"
)

func TestDarwinSourceMapsSyntheticEventsToCanonicalChords(t *testing.T) {
	chords := []string{"M-1", "C-1", "M-S-a", "M-F5", "M-Left"}
	bindings := ParseBindings(chords)
	if len(bindings) != len(chords) {
		t.Fatalf("ParseBindings(%v) returned %d bindings, want %d", chords, len(bindings), len(chords))
	}

	input := make(chan keyEvent, 7)
	source := newDarwinSource(func() (keyEvent, bool) {
		event, ok := <-input
		return event, ok
	})
	if err := source.Replace(bindings); err != nil {
		t.Fatalf("Replace() error = %v", err)
	}

	done := make(chan struct{})
	go func() {
		source.readEvents()
		close(done)
	}()

	for _, event := range []keyEvent{
		{keyCode: 18, modifiers: ModifierAlt},
		{keyCode: 18, modifiers: ModifierControl},
		{keyCode: 0, modifiers: ModifierAlt | ModifierShift},
		{keyCode: 96, modifiers: ModifierAlt},
		{keyCode: 123, modifiers: ModifierAlt},
		{keyCode: 47, modifiers: ModifierAlt},
		{keyCode: 0},
	} {
		input <- event
	}
	close(input)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("readEvents() did not stop after the synthetic event source closed")
	}

	var got []string
	for {
		select {
		case chord := <-source.Events():
			got = append(got, chord)
		default:
			if !reflect.DeepEqual(got, chords) {
				t.Fatalf("Events() = %v, want %v", got, chords)
			}
			return
		}
	}
}
