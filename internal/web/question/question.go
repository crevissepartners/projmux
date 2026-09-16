// Package question answers a Claude Code AskUserQuestion from the browser by
// pressing the question widget's own keys.
//
// This is the one place the web client puts keys into an agent's Pane.
// projmux otherwise keeps agent input off raw pane keys; the operator chose
// this contained exception because Claude Code has no answer channel a third
// party can call. It is to be replaced by that channel when one exists.
//
// The widget is a numbered selection list, not a text prompt. A pasted label
// is ignored, and an Enter after it selects whatever the cursor was on. Its
// keys, measured against a live session:
//
//   - one single-select question: the option's number selects and submits.
//   - several questions: a number answers the current tab and moves on.
//   - multi-select: each number toggles; Tab moves on.
//   - free text: the "Type something" number focuses the field, the text is
//     typed, Enter commits it.
//   - after the last question, when there is more than one or any is
//     multi-select: a review screen, where `1` submits.
//
// Every step that could land on the wrong screen is checked against a capture
// of the pane first, so a question already answered, or a widget in a state
// these keys do not expect, is a refusal instead of keystrokes into whatever
// is there.
package question

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/crevissepartners/projmux/internal/web/transcript"
)

// ToolName is the tool call a question arrives as.
const ToolName = "AskUserQuestion"

// Refusal codes. They are stable tokens the web API passes through.
const (
	CodeNoPending   = "question-not-pending"
	CodeAnswered    = "question-answered"
	CodeUnreadable  = "question-unreadable"
	CodeInvalid     = "question-invalid-answer"
	CodeUnmeasured  = "question-unsupported-answer"
	CodeNotOnScreen = "question-not-on-screen"
	CodeNotReviewed = "question-review-not-shown"
	CodeWrongPane   = "question-wrong-pane"
)

// Refusal is a question the keys were not sent for, or not all of them.
type Refusal struct {
	Code    string
	Message string
}

func (r *Refusal) Error() string { return r.Code + ": " + r.Message }

func refuse(code, format string, args ...any) error {
	return &Refusal{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Question is the part of a question the keys depend on.
type Question struct {
	Question    string `json:"question"`
	MultiSelect bool   `json:"multiSelect"`
	Options     []struct {
		Label string `json:"label"`
	} `json:"options"`
}

// Answer is one question's answer: option indexes, and optional free text.
type Answer struct {
	Picks []int  `json:"picks"`
	Other string `json:"other"`
}

// Runner runs tmux. internal/integrations/tmux.ExecRunner satisfies it.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Pending finds the newest question in a transcript that has not been
// answered yet. The question's shape is read here, from the agent's own
// record, never from the request.
func Pending(turns []transcript.Turn) (id string, questions []Question, err error) {
	for i := len(turns) - 1; i >= 0; i-- {
		for _, call := range turns[i].Tools {
			if call.Name != ToolName {
				continue
			}
			if call.Result != "" {
				return "", nil, refuse(CodeAnswered, "the newest question already has an answer")
			}
			var input struct {
				Questions []Question `json:"questions"`
			}
			if err := json.Unmarshal([]byte(call.Input), &input); err != nil || len(input.Questions) == 0 {
				return "", nil, refuse(CodeUnreadable, "the question input could not be read")
			}
			return call.ID, input.Questions, nil
		}
	}
	return "", nil, refuse(CodeNoPending, "there is no pending question")
}

var paneIDPattern = regexp.MustCompile(`^%\d+$`)

// Pane is the exact pane to answer in: its tmux id, the server it lives on,
// and the projmux uid it must carry.
type Pane struct {
	Runner  Runner
	Server  []string
	ID      string
	UID     string
	stepGap time.Duration
}

// Check refuses answers the widget would not take, before any key is sent.
func Check(questions []Question, answers []Answer) error {
	if len(answers) != len(questions) {
		return refuse(CodeInvalid, "%d questions, %d answers", len(questions), len(answers))
	}
	for i, q := range questions {
		a := answers[i]
		other := strings.TrimSpace(a.Other)
		if q.MultiSelect {
			if other != "" {
				// Not measured: whether the free-text row toggles or takes
				// focus in a multi-select list. Guessing would type into an
				// unknown state.
				return refuse(CodeUnmeasured, "free text on a multi-select question is answered in the terminal")
			}
			if len(a.Picks) == 0 {
				return refuse(CodeInvalid, "question %d has nothing picked", i+1)
			}
		} else if (len(a.Picks) == 1) == (other != "") {
			return refuse(CodeInvalid, "question %d takes exactly one pick or free text", i+1)
		}
		for _, pick := range a.Picks {
			if pick < 0 || pick >= len(q.Options) {
				return refuse(CodeInvalid, "question %d has no option %d", i+1, pick+1)
			}
		}
		for _, r := range other {
			if unicode.IsControl(r) {
				return refuse(CodeInvalid, "free text may not contain control characters")
			}
		}
	}
	return nil
}

// Answer drives the widget in one pane.
func (p Pane) Answer(ctx context.Context, questions []Question, answers []Answer) error {
	if err := Check(questions, answers); err != nil {
		return err
	}
	if p.Runner == nil || !paneIDPattern.MatchString(p.ID) || p.UID == "" {
		return refuse(CodeWrongPane, "pane %q is not an exact tmux pane", p.ID)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// The id is a tmux handle the Registry recorded; the pane must still be
	// the one projmux mirrored its uid onto before anything is pressed.
	owner, err := p.tmux(ctx, "display-message", "-p", "-t", p.ID, "#{@projmux_pane_uid}")
	if err != nil || strings.TrimSpace(owner) != p.UID {
		return refuse(CodeWrongPane, "pane %s is not %s", p.ID, p.UID)
	}
	screen, err := p.tmux(ctx, "capture-pane", "-p", "-t", p.ID)
	if err != nil {
		return fmt.Errorf("read pane %s: %w", p.ID, err)
	}
	if !strings.Contains(screen, "Enter to select") || !strings.Contains(screen, firstLine(questions[0].Question)) {
		return refuse(CodeNotOnScreen, "the question is not on screen; it may already be answered")
	}

	review := len(questions) > 1
	for i, q := range questions {
		a := answers[i]
		other := strings.TrimSpace(a.Other)
		switch {
		case other != "":
			// "Type something" is numbered right after the options.
			if err := p.keys(ctx, true, strconv.Itoa(len(q.Options)+1)); err != nil {
				return err
			}
			if err := p.keys(ctx, true, other); err != nil {
				return err
			}
			if err := p.keys(ctx, false, "Enter"); err != nil {
				return err
			}
		case q.MultiSelect:
			review = true
			for _, pick := range a.Picks {
				if err := p.keys(ctx, true, strconv.Itoa(pick+1)); err != nil {
					return err
				}
			}
			if err := p.keys(ctx, false, "Tab"); err != nil {
				return err
			}
		default:
			if err := p.keys(ctx, true, strconv.Itoa(a.Picks[0]+1)); err != nil {
				return err
			}
		}
	}
	if !review {
		return nil
	}
	p.pause(ctx, 2*p.gap())
	screen, err = p.tmux(ctx, "capture-pane", "-p", "-t", p.ID)
	if err != nil {
		return fmt.Errorf("read pane %s: %w", p.ID, err)
	}
	if !strings.Contains(screen, "Ready to submit your answers?") {
		// The answers are entered but not sent. Pressing `1` on a screen this
		// code did not expect could pick an option instead.
		return refuse(CodeNotReviewed, "the answers are entered but the submit screen did not appear; finish in the terminal")
	}
	return p.keys(ctx, true, "1")
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	// The widget wraps long questions; the first few words are enough to
	// recognize the screen and survive the wrap.
	if runes := []rune(line); len(runes) > 12 {
		line = string(runes[:12])
	}
	return line
}

func (p Pane) gap() time.Duration {
	if p.stepGap > 0 {
		return p.stepGap
	}
	return 350 * time.Millisecond
}

// keys sends one step and waits for the widget to redraw. literal sends text
// as typed; otherwise the value is a tmux key name.
func (p Pane) keys(ctx context.Context, literal bool, value string) error {
	args := []string{"send-keys", "-t", p.ID}
	if literal {
		args = append(args, "-l", "--")
	}
	args = append(args, value)
	if _, err := p.tmux(ctx, args...); err != nil {
		return fmt.Errorf("send keys to %s: %w", p.ID, err)
	}
	p.pause(ctx, p.gap())
	return nil
}

func (p Pane) tmux(ctx context.Context, args ...string) (string, error) {
	full := append(append([]string{}, p.Server...), args...)
	out, err := p.Runner.Run(ctx, "tmux", full...)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func (p Pane) pause(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// IsRefusal reports whether err is a Refusal and returns it.
func IsRefusal(err error) (*Refusal, bool) {
	var refusal *Refusal
	ok := errors.As(err, &refusal)
	return refusal, ok
}
