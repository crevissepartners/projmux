package picker

import "strings"

// RecorderPhase is the visible state of a purpose-built keystroke recorder.
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
	// NormalizeStroke is the sequence-aware normalization seam. The index is
	// the zero-based position the new stroke would occupy. When it is nil,
	// Normalize is used for every stroke.
	NormalizeStroke func(RecorderKey, int) (string, error)
	Validate        func(string) error
	// AutoConfirm returns the first valid candidate immediately. Delivery
	// observation uses it to read one logical stroke without reserving a second
	// key as a finish control.
	AutoConfirm bool
	// CaptureEnter treats plain Enter as a candidate when AutoConfirm is set.
	// Escape remains the recorder cancel control.
	CaptureEnter bool
	State        RecorderState
}

type RecorderState struct {
	Phase     RecorderPhase
	Strokes   []string
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
	recorderBackspace
)

type recorderEvent struct {
	kind recorderEventKind
	key  RecorderKey
}

func newRecorderState() RecorderState {
	return RecorderState{Phase: RecorderRecording}
}

func reduceRecorderStateAt(state RecorderState, event recorderEvent, normalize func(RecorderKey, int) (string, error), validate func(string) error) (RecorderState, recorderOutcome) {
	if state.Phase == "" {
		state = newRecorderState()
	}
	if len(state.Strokes) == 0 && strings.TrimSpace(state.Candidate) != "" {
		state.Strokes = strings.Fields(state.Candidate)
	}
	switch event.kind {
	case recorderEscape:
		return state, recorderCancel
	case recorderBackspace:
		if len(state.Strokes) == 0 {
			return state, recorderContinue
		}
		state.Strokes = state.Strokes[:len(state.Strokes)-1]
		state.Candidate = strings.Join(state.Strokes, " ")
		state.Message = ""
		if len(state.Strokes) == 0 {
			state.Phase = RecorderRecording
		} else {
			state.Phase = RecorderStaged
		}
		return state, recorderContinue
	case recorderEnter:
		if strings.TrimSpace(state.Candidate) == "" {
			return state, recorderContinue
		}
		if validate != nil {
			if err := validate(state.Candidate); err != nil {
				state.Message = "Cannot save: " + err.Error() + ". Choose another key or use Enter binding manually."
				return state, recorderContinue
			}
		}
		state.Phase = RecorderConfirmed
		state.Message = ""
		return state, recorderConfirm
	case recorderCandidate:
		if len(state.Strokes) >= 4 {
			state.Message = "Maximum 4 strokes. Press Enter to save or Backspace to remove the last stroke."
			return state, recorderContinue
		}
		if normalize == nil {
			state.Message = "Cannot record this input. Choose another key or use Enter binding manually."
			return state, recorderContinue
		}
		candidate, err := normalize(event.key, len(state.Strokes))
		candidate = strings.TrimSpace(candidate)
		if err != nil || candidate == "" {
			detail := "Cannot record this input"
			if err != nil && strings.TrimSpace(err.Error()) != "" {
				detail += ": " + err.Error()
			}
			state.Message = detail + ". Choose another key or use Enter binding manually."
			return state, recorderContinue
		}
		state.Phase = RecorderStaged
		state.Strokes = append(state.Strokes, candidate)
		state.Candidate = strings.Join(state.Strokes, " ")
		state.Message = ""
		return state, recorderContinue
	default:
		return state, recorderContinue
	}
}
