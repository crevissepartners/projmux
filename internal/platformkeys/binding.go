package platformkeys
import "github.com/crevissepartners/projmux/internal/platformkeys/binding"
type Modifiers = binding.Modifiers
type Binding = binding.Binding
const (
 ModifierAlt = binding.ModifierAlt
 ModifierControl = binding.ModifierControl
 ModifierShift = binding.ModifierShift
 ModifierCommand = binding.ModifierCommand
)
func ParseBinding(chord string) (Binding, bool) { return binding.ParseBinding(chord) }
func ParseBindings(chords []string) []Binding { return binding.ParseBindings(chords) }
