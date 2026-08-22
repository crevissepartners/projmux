package app

import (
	"fmt"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
)

// The Projects sidebar's Runtime diagnostics preference.
//
// This is the settings adapter half of the Alt-1 Runtime row policy: it resolves
// the effective mode and the source annotation Settings renders. The row
// decision itself is the pure reducer in switch_registry_rows.go, and the
// Registry projection that produces the row and its tally is untouched by both
// -- `registryview` still emits complete Runtime counts and a complete Runtime
// row, because a presentation preference must not weaken the diagnostics
// contract every other consumer reads.

// runtimeDiagnosticsVisibilityLabelWhenNeeded and
// runtimeDiagnosticsVisibilityLabelAlways are the two user-facing choices. They
// name what the operator sees rather than the boolean underneath, so the row
// reads as a policy and not as a feature flag.
const (
	runtimeDiagnosticsVisibilityLabelWhenNeeded = "When needed"
	runtimeDiagnosticsVisibilityLabelAlways     = "Always"
)

// runtimeDiagnosticsVisibilitySourceInvalid is the source annotation of a saved
// value the choice does not name. It is distinct from `default` on purpose: the
// effective behavior is the same and the reason an operator has to fix is not.
const runtimeDiagnosticsVisibilitySourceInvalid = "invalid saved value; using default"

// settingsRuntimeDiagnosticsVisibilityDetail opens the chooser. The two set
// actions are the mode spellings themselves, so the saved file and the picker
// value can never drift apart.
const settingsRuntimeDiagnosticsVisibilityDetail = settingsActionPrefixRuntimeDiagnostics + "view"

// runtimeDiagnosticsVisibilityState is one resolved preference read.
type runtimeDiagnosticsVisibilityState struct {
	Mode   config.RuntimeDiagnosticsVisibility
	Origin config.RuntimeDiagnosticsVisibilityOrigin
}

// Source renders the origin the way every other Settings row renders one.
func (s runtimeDiagnosticsVisibilityState) Source() string {
	switch s.Origin {
	case config.RuntimeDiagnosticsVisibilitySaved:
		return "saved"
	case config.RuntimeDiagnosticsVisibilityInvalid:
		return runtimeDiagnosticsVisibilitySourceInvalid
	default:
		return "default"
	}
}

// runtimeDiagnosticsVisibilityChoiceLabel is the display literal of one mode.
func runtimeDiagnosticsVisibilityChoiceLabel(mode config.RuntimeDiagnosticsVisibility) string {
	if mode == config.RuntimeDiagnosticsAlways {
		return runtimeDiagnosticsVisibilityLabelAlways
	}
	return runtimeDiagnosticsVisibilityLabelWhenNeeded
}

// currentRuntimeDiagnosticsVisibility resolves the effective preference.
//
// Every failure resolves to the shipped default rather than to `Always`: a
// preference that cannot be read must not add a row the operator asked to be
// contextual, and it must not hide one either, which is why the needed predicate
// still shows the row whenever the observation itself is unavailable.
func currentRuntimeDiagnosticsVisibility(homeDir func() (string, error), lookupEnv func(string) string) runtimeDiagnosticsVisibilityState {
	paths, err := configPaths(homeDir, lookupEnv)
	if err != nil {
		return runtimeDiagnosticsVisibilityState{
			Mode:   config.RuntimeDiagnosticsVisibilityDefault,
			Origin: config.RuntimeDiagnosticsVisibilityDefaulted,
		}
	}
	mode, origin, err := config.LoadRuntimeDiagnosticsVisibilityFile(paths.RuntimeDiagnosticsVisibilityFile())
	if err != nil {
		return runtimeDiagnosticsVisibilityState{
			Mode:   config.RuntimeDiagnosticsVisibilityDefault,
			Origin: config.RuntimeDiagnosticsVisibilityInvalid,
		}
	}
	return runtimeDiagnosticsVisibilityState{Mode: mode, Origin: origin}
}

// setRuntimeDiagnosticsVisibility saves one valid choice.
//
// An unknown action is an error rather than a silent normalization: the two
// choices are the only values the picker can produce, so anything else is a
// wiring bug and not an operator's typo to be guessed at.
func (c *settingsCommand) setRuntimeDiagnosticsVisibility(value string) error {
	mode, ok := config.NormalizeRuntimeDiagnosticsVisibility(value)
	if !ok {
		return fmt.Errorf("unknown runtime diagnostics visibility action: %s", value)
	}
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return err
	}
	if err := config.SaveRuntimeDiagnosticsVisibilityFile(paths.RuntimeDiagnosticsVisibilityFile(), mode); err != nil {
		return err
	}
	if c.lookupEnv != nil && strings.TrimSpace(c.lookupEnv("TMUX")) != "" && c.runCommand != nil {
		_ = c.runCommand("tmux", "display-message", "runtime diagnostics row: "+string(mode))
	}
	return nil
}
