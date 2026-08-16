package picker

import "strings"

// RecorderPhase is the visible state of a purpose-built single-chord recorder.
// Confirmation is terminal: the picker returns immediately after entering it.
type RecorderPhase string

const (
	RecorderRecording RecorderPhase = "recording"
	RecorderStaged    RecorderPhase = "staged"
	RecorderConfirmed RecorderPhase = "confirmed"
)

// RecorderKey is one decoded native-picker input event. Name contains picker
// key names such as "ctrl-a" or "alt-shift-left"; Text contains a single
// printable rune. Plain Enter and Escape are represented as control events by
// the interactive loop and are never passed to Normalize.
type RecorderKey struct {
	Name string
	Text string
}

// RecorderOptions adds a recorder state slice to the native picker's existing
// input loop. Normalize remains owned by the caller so picker input decoding
// does not acquire keymap policy. Validate is called only on explicit Enter and
// must not write configuration.
type RecorderOptions struct {
	Normalize func(RecorderKey) (string, error)
	Validate  func(string) error
	State     RecorderState
}

type RecorderState struct {
	Phase     RecorderPhase
	Candidate string
	Message   string
}

type recorderOutcome int

const (
	recorderContinue recorderOutcome = iota
	recorderConfirm
	recorderCancel
)

type recorderEventKind int

const (
	recorderCandidate recorderEventKind = iota
	recorderEnter
	recorderEscape
)

type recorderEvent struct {
	kind recorderEventKind
	key  RecorderKey
}

func newRecorderState() RecorderState {
	return RecorderState{Phase: RecorderRecording}
}

func reduceRecorderState(state RecorderState, event recorderEvent, normalize func(RecorderKey) (string, error), validate func(string) error) (RecorderState, recorderOutcome) {
	if state.Phase == "" {
		state = newRecorderState()
	}
	switch event.kind {
	case recorderEscape:
		return state, recorderCancel
	case recorderEnter:
		if strings.TrimSpace(state.Candidate) == "" {
			return state, recorderContinue
		}
		if validate != nil {
			if err := validate(state.Candidate); err != nil {
				state.Message = "Cannot save: " + err.Error() + ". Choose another key or use Enter key name manually."
				return state, recorderContinue
			}
		}
		state.Phase = RecorderConfirmed
		state.Message = ""
		return state, recorderConfirm
	case recorderCandidate:
		if normalize == nil {
			state.Message = "Cannot record this input. Choose another key or use Enter key name manually."
			return state, recorderContinue
		}
		candidate, err := normalize(event.key)
		candidate = strings.TrimSpace(candidate)
		if err != nil || candidate == "" {
			detail := "Cannot record this input"
			if err != nil && strings.TrimSpace(err.Error()) != "" {
				detail += ": " + err.Error()
			}
			state.Message = detail + ". Choose another key or use Enter key name manually."
			return state, recorderContinue
		}
		state.Phase = RecorderStaged
		state.Candidate = candidate
		state.Message = ""
		return state, recorderContinue
	default:
		return state, recorderContinue
	}
}
