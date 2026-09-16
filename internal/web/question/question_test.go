package question

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/web/transcript"
)

var _ Runner = inttmux.ExecRunner{}

// scriptRunner answers tmux reads from fixed screens and records every key.
type scriptRunner struct {
	owner   string
	screens []string
	keys    []string
}

func (s *scriptRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	if name != "tmux" || len(args) < 2 || args[0] != "-L" {
		return nil, errors.New("unexpected call")
	}
	args = args[2:]
	switch args[0] {
	case "display-message":
		return []byte(s.owner + "\n"), nil
	case "capture-pane":
		if len(s.screens) == 0 {
			return nil, errors.New("no screen")
		}
		screen := s.screens[0]
		if len(s.screens) > 1 {
			s.screens = s.screens[1:]
		}
		return []byte(screen), nil
	case "send-keys":
		s.keys = append(s.keys, strings.Join(args[3:], " "))
		return nil, nil
	}
	return nil, errors.New("unexpected verb " + args[0])
}

func q(text string, multi bool, n int) Question {
	out := Question{Question: text, MultiSelect: multi}
	for range n {
		out.Options = append(out.Options, struct {
			Label string `json:"label"`
		}{Label: "o"})
	}
	return out
}

const widget = "Which colour?\n 1. red\n 2. blue\nEnter to select"

func pane(r *scriptRunner) Pane {
	return Pane{Runner: r, Server: []string{"-L", "projmux"}, ID: "%7", UID: "pane-a", stepGap: 1}
}

func TestAnswerPressesTheWidgetsOwnKeys(t *testing.T) {
	cases := []struct {
		name    string
		qs      []Question
		as      []Answer
		screens []string
		want    []string
	}{
		{"one single-select submits with its number",
			[]Question{q("Which colour?", false, 2)}, []Answer{{Picks: []int{1}}},
			[]string{widget}, []string{"-l -- 2"}},
		{"free text is the next number, the text, then Enter",
			[]Question{q("Which colour?", false, 2)}, []Answer{{Other: "green"}},
			[]string{widget}, []string{"-l -- 3", "-l -- green", "Enter"}},
		{"several questions end on the review screen",
			[]Question{q("Which colour?", false, 2), q("Size?", false, 3)}, []Answer{{Picks: []int{0}}, {Picks: []int{2}}},
			[]string{widget, "Ready to submit your answers?"}, []string{"-l -- 1", "-l -- 3", "-l -- 1"}},
		{"multi-select toggles, tabs on and reviews",
			[]Question{q("Which colour?", true, 3)}, []Answer{{Picks: []int{0, 2}}},
			[]string{widget, "Ready to submit your answers?"}, []string{"-l -- 1", "-l -- 3", "Tab", "-l -- 1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &scriptRunner{owner: "pane-a", screens: tc.screens}
			if err := pane(r).Answer(context.Background(), tc.qs, tc.as); err != nil {
				t.Fatal(err)
			}
			if strings.Join(r.keys, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("keys = %q, want %q", r.keys, tc.want)
			}
		})
	}
}

func TestAnswerRefusesBeforeAnyKey(t *testing.T) {
	cases := map[string]struct {
		qs      []Question
		as      []Answer
		owner   string
		screens []string
		code    string
	}{
		"count mismatch":             {[]Question{q("Which colour?", false, 2)}, nil, "pane-a", []string{widget}, CodeInvalid},
		"two picks on single":        {[]Question{q("Which colour?", false, 3)}, []Answer{{Picks: []int{0, 1}}}, "pane-a", []string{widget}, CodeInvalid},
		"pick and text on single":    {[]Question{q("Which colour?", false, 3)}, []Answer{{Picks: []int{0}, Other: "x"}}, "pane-a", []string{widget}, CodeInvalid},
		"nothing on single":          {[]Question{q("Which colour?", false, 3)}, []Answer{{}}, "pane-a", []string{widget}, CodeInvalid},
		"out of range":               {[]Question{q("Which colour?", false, 2)}, []Answer{{Picks: []int{2}}}, "pane-a", []string{widget}, CodeInvalid},
		"nothing on multi":           {[]Question{q("Which colour?", true, 2)}, []Answer{{}}, "pane-a", []string{widget}, CodeInvalid},
		"text on multi (unmeasured)": {[]Question{q("Which colour?", true, 2)}, []Answer{{Picks: []int{0}, Other: "x"}}, "pane-a", []string{widget}, CodeUnmeasured},
		"control char in text":       {[]Question{q("Which colour?", false, 2)}, []Answer{{Other: "a\x1bb"}}, "pane-a", []string{widget}, CodeInvalid},
		"another pane's uid":         {[]Question{q("Which colour?", false, 2)}, []Answer{{Picks: []int{0}}}, "pane-b", []string{widget}, CodeWrongPane},
		"question not on screen":     {[]Question{q("Which colour?", false, 2)}, []Answer{{Picks: []int{0}}}, "pane-a", []string{"$ "}, CodeNotOnScreen},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := &scriptRunner{owner: tc.owner, screens: tc.screens}
			err := pane(r).Answer(context.Background(), tc.qs, tc.as)
			refusal, ok := IsRefusal(err)
			if !ok || refusal.Code != tc.code {
				t.Fatalf("err = %v, want %s", err, tc.code)
			}
			if len(r.keys) != 0 {
				t.Fatalf("keys were sent: %q", r.keys)
			}
		})
	}
	bad := Pane{Runner: &scriptRunner{}, Server: []string{"-L", "projmux"}, ID: "window-1", UID: "pane-a"}
	if _, ok := IsRefusal(bad.Answer(context.Background(), []Question{q("Which colour?", false, 2)}, []Answer{{Picks: []int{0}}})); !ok {
		t.Fatal("a non-pane id was not refused")
	}
}

func TestAnswerLeavesEnteredAnswersUnsentWithoutTheReviewScreen(t *testing.T) {
	r := &scriptRunner{owner: "pane-a", screens: []string{widget, "something else"}}
	err := pane(r).Answer(context.Background(), []Question{q("Which colour?", false, 2), q("Size?", false, 2)}, []Answer{{Picks: []int{0}}, {Picks: []int{1}}})
	if refusal, ok := IsRefusal(err); !ok || refusal.Code != CodeNotReviewed {
		t.Fatalf("err = %v", err)
	}
	if strings.Join(r.keys, "|") != "-l -- 1|-l -- 2" {
		t.Fatalf("keys = %q, want the answers and no submit", r.keys)
	}
}

func TestPendingReadsTheNewestUnansweredQuestion(t *testing.T) {
	input := `{"questions":[{"question":"Which colour?","multiSelect":false,"options":[{"label":"red"}]}]}`
	turns := []transcript.Turn{
		{Tools: []transcript.ToolCall{{ID: "old", Name: ToolName, Input: input, Result: "red"}}},
		{Tools: []transcript.ToolCall{{ID: "new", Name: ToolName, Input: input}}},
	}
	id, questions, err := Pending(turns)
	if err != nil || id != "new" || len(questions) != 1 || questions[0].Question != "Which colour?" {
		t.Fatalf("Pending = %q %v %v", id, questions, err)
	}
	if _, _, err := Pending(turns[:1]); err == nil {
		t.Fatal("an answered question is pending")
	}
	if _, _, err := Pending(nil); err == nil {
		t.Fatal("no question is pending")
	}
	broken := []transcript.Turn{{Tools: []transcript.ToolCall{{ID: "x", Name: ToolName, Input: "{"}}}}
	if refusal, ok := IsRefusal(func() error { _, _, err := Pending(broken); return err }()); !ok || refusal.Code != CodeUnreadable {
		t.Fatal("clipped input was not refused as unreadable")
	}
}

// TestAnswerQuestionLive drives a real Claude Code question widget. It needs
// a live pane with a question on screen, so it runs only when pointed at one:
//
//	PROJMUX_WEB_ANSWER_PANE=%12 PROJMUX_WEB_ANSWER_UID=pane-… \
//	PROJMUX_WEB_ANSWER_SERVER=projmux \
//	PROJMUX_WEB_ANSWER_QUESTIONS='[{"question":"Colour?","options":[{"label":"red"},{"label":"blue"}]}]' \
//	PROJMUX_WEB_ANSWER='[{"picks":[1]}]' go test ./internal/web/question -run TestAnswerQuestionLive -v
//
// The protocol was measured against the widget, not read from
// documentation, which is why it is kept runnable.
func TestAnswerQuestionLive(t *testing.T) {
	id := os.Getenv("PROJMUX_WEB_ANSWER_PANE")
	if id == "" {
		t.Skip("PROJMUX_WEB_ANSWER_PANE not set")
	}
	var questions []Question
	if err := json.Unmarshal([]byte(os.Getenv("PROJMUX_WEB_ANSWER_QUESTIONS")), &questions); err != nil {
		t.Fatalf("PROJMUX_WEB_ANSWER_QUESTIONS: %v", err)
	}
	var answers []Answer
	if err := json.Unmarshal([]byte(os.Getenv("PROJMUX_WEB_ANSWER")), &answers); err != nil {
		t.Fatalf("PROJMUX_WEB_ANSWER: %v", err)
	}
	live := Pane{
		Runner: inttmux.ExecRunner{},
		Server: []string{"-L", os.Getenv("PROJMUX_WEB_ANSWER_SERVER")},
		ID:     id,
		UID:    os.Getenv("PROJMUX_WEB_ANSWER_UID"),
	}
	if err := live.Answer(context.Background(), questions, answers); err != nil {
		t.Fatalf("Answer: %v", err)
	}
}
