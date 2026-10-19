package platformkeys
import ("reflect";"testing")
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
