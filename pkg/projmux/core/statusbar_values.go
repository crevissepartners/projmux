package core

// StatusbarVisibility is the effective value of one status bar visibility
// leaf. It is the text of ConfigVisibilityState.Effective and
// ConfigToggleState.Effective.
type StatusbarVisibility string

// StatusbarVisibilitySource names where a visibility value came from. It is
// the text of ConfigVisibilityState.Source and ConfigToggleState.Source.
type StatusbarVisibilitySource string

const (
	StatusbarVisibilityOn  StatusbarVisibility = "on"
	StatusbarVisibilityOff StatusbarVisibility = "off"

	// StatusbarVisibilitySourceDefault is a built-in default, Saved a value
	// stored by the leaf's own setting, and Central a value read from the
	// central statusbar defaults file.
	StatusbarVisibilitySourceDefault StatusbarVisibilitySource = "default"
	StatusbarVisibilitySourceSaved   StatusbarVisibilitySource = "saved"
	StatusbarVisibilitySourceCentral StatusbarVisibilitySource = "central"
)

// StatusbarVisibilityState projects a stored visibility value separately from
// the effective one. Invalid saved text is kept in Invalid while Effective
// falls back to on.
type StatusbarVisibilityState struct {
	Effective StatusbarVisibility
	Saved     string
	Source    StatusbarVisibilitySource
	Invalid   string
}

// DefaultStatusbarVisibilityState is the state of a leaf with nothing stored:
// Effective on, Source default.
func DefaultStatusbarVisibilityState() StatusbarVisibilityState {
	return StatusbarVisibilityState{Effective: StatusbarVisibilityOn, Source: StatusbarVisibilitySourceDefault}
}

const (
	// AgentQuestionWindowUnlimitedWord is the ConfigValue.Window text of a
	// question window that lasts until the question is answered.
	AgentQuestionWindowUnlimitedWord = "unlimited"

	// AgentQuestionWindowUnlimitedSeconds is the number of seconds that
	// window is installed as. It is derived from the question hook timeout,
	// so a copy of the number goes stale when that timeout changes.
	AgentQuestionWindowUnlimitedSeconds = 604785
)
