package app

import (
	"context"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
)

// The new Window consumer's disclosure layer.
//
// Both UI create producers write what a committed create could not carry over
// to their own stderr, with one shared call (writeIntentAgentNotices). Only the
// consumer decides whether the operator ever reads it. These tests pin the new
// Window consumer -- (*tmuxCommand).runWindowCreateIntent and the commit half of
// finishWindowIntent -- against the split funnel that already shows it, so the
// two paths cannot drift apart again without a test saying so.

// windowFunnelClientLines runs one resume-picker answer through the real
// generated Window route, from the pressed key to the line the pressing client
// is shown. The canonical create underneath is the fixture's own, so the stderr
// the route consumes is the stderr the producer really writes.
func (f pickerLaunchValuesFixture) windowFunnelClientLines(t *testing.T, client string, intent agentPaneIntent) []string {
	t.Helper()
	origin, _, _ := f.tmux.pane(f.originID)
	if origin == nil {
		t.Fatal("fixture origin Pane has no runtime Session")
	}
	f.tmux.attachClient(client, origin)
	cmd := &tmuxCommand{
		runner:       f.tmux,
		windowCreate: f.create.createWindowFromIntent,
		launchChoose: func(string, string) launchChoice { return launchChoice{intent: intent} },
	}
	if err := cmd.Run([]string{"window-create", "--client", client, "--anchor", f.originID}, ioDiscard{}, ioDiscard{}); err != nil {
		t.Fatalf("Window funnel route: %v", err)
	}
	var lines []string
	for _, message := range f.tmux.clientMessages {
		if message.client == client {
			lines = append(lines, message.text)
		}
	}
	return lines
}

// splitFunnelClientLines runs the same answer through the split UI funnel,
// (*aiCommand).createPaneFromIntent, with no client attached so the focus step
// has nothing to move and the notice is the one line the funnel shows.
func (f pickerLaunchValuesFixture) splitFunnelClientLines(t *testing.T, client string, intent agentPaneIntent) []string {
	t.Helper()
	var displayed [][]string
	ai := &aiCommand{
		panes: f.create,
		lookupEnv: func(key string) string {
			switch key {
			case canonicalCreateTargetClientEnv:
				return client
			case "TMUX_SPLIT_TARGET_PANE":
				return f.originID
			}
			return ""
		},
		readCommand: func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
		runCommand: func(_ context.Context, name string, args ...string) error {
			displayed = append(displayed, append([]string{name}, args...))
			return nil
		},
	}
	if err := ai.createPaneFromIntent(intent); err != nil {
		t.Fatalf("split UI funnel: %v", err)
	}
	var lines []string
	for _, argv := range displayed {
		if len(argv) < 5 || argv[0] != "tmux" || argv[1] != "display-message" || argv[2] != "-c" || argv[3] != client {
			t.Fatalf("split funnel tmux call = %v, want a display-message on %s", argv, client)
		}
		lines = append(lines, argv[len(argv)-1])
	}
	return lines
}

// pickerAnswer is the resume-picker answer both funnels are handed.
func pickerAnswer(conversation string) agentPaneIntent {
	return agentPaneIntent{
		producer: canonicalProducerResumePicker, provider: aiModeClaude, placement: "right",
		conversationID: conversation,
	}
}

// oneClientLine fails unless the funnel showed exactly one line.
func oneClientLine(t *testing.T, funnel string, lines []string) string {
	t.Helper()
	if len(lines) != 1 {
		t.Fatalf("%s showed %d client lines %q, want exactly one", funnel, len(lines), lines)
	}
	return lines[0]
}

// disclosureCarriedBy is the notice half of one funnel's client line: the line
// with the lead its own surface owns removed. The split funnel shows the notice
// alone behind `projmux: `; the Window funnel rides it on the bounded success
// line the surface already owed the client, the way the pane-menu split does
// (tmux.go). What must match between them is what is left.
func disclosureCarriedBy(t *testing.T, funnel, line, lead string) string {
	t.Helper()
	rest, found := strings.CutPrefix(line, lead)
	if !found {
		t.Fatalf("%s client line = %q, want it to lead with %q", funnel, line, lead)
	}
	if strings.Contains(line, "projmux: projmux:") {
		t.Fatalf("%s client line = %q carries a doubled prefix", funnel, line)
	}
	return strings.TrimSpace(rest)
}

// TestResumePickerDisclosureReachesTheClientThroughBothCreateFunnels is the
// new Window consumer's half of C-1, contrasted in one test with the split
// funnel that already satisfied it. The same holders and the same answer go
// through both producers; both pressing clients learn the same thing.
//
// The disagreeing-holder sample is compared byte for byte: its notice names the
// holders, so neither funnel can make it differ. The unrepassable-bundle sample
// names the new Agent each funnel created, so it is compared by the reasons the
// operator acts on, each carried exactly once.
func TestResumePickerDisclosureReachesTheClientThroughBothCreateFunnels(t *testing.T) {
	t.Parallel()
	const client = "/dev/pts/9"
	t.Run("holders that disagree", func(t *testing.T) {
		t.Parallel()
		bundle := claudeLaunchBundle(map[string]string{
			coremetadata.AnnotationAgentPersona: "go-reviewer", coremetadata.AnnotationAgentPersonaDigest: "sha256:0",
		}, "low")
		hold := func(f pickerLaunchValuesFixture) {
			f.hold(t, "agt-alpha-codex", aiModeClaude, personaResumeConversation, bundle, nil)
			f.hold(t, "agt-beta-codex", aiModeClaude, personaResumeConversation, claudeLaunchBundle(bundle, "high"), nil)
		}
		split := newPickerLaunchValuesFixture(t, nil)
		hold(split)
		window := newPickerLaunchValuesFixture(t, split.planner)
		hold(window)

		splitLine := oneClientLine(t, "split funnel", split.splitFunnelClientLines(t, client, pickerAnswer(personaResumeConversation)))
		windowLine := oneClientLine(t, "Window funnel", window.windowFunnelClientLines(t, client, pickerAnswer(personaResumeConversation)))

		for funnel, line := range map[string]string{"split funnel": splitLine, "Window funnel": windowLine} {
			if got := strings.Count(line, launchValuesReasonAmbiguous); got != 1 {
				t.Fatalf("%s client line = %q carries %s %d times, want exactly once", funnel, line, launchValuesReasonAmbiguous, got)
			}
		}
		splitNotice := disclosureCarriedBy(t, "split funnel", splitLine, "projmux: ")
		windowNotice := disclosureCarriedBy(t, "Window funnel", windowLine, windowCreatedMessage+": ")
		if splitNotice != windowNotice {
			t.Fatalf("the two producers disclose different things to the same client:\n split  %q\n Window %q", splitNotice, windowNotice)
		}
	})

	t.Run("a bundle the create cannot repass", func(t *testing.T) {
		t.Parallel()
		bundle := map[string]string{
			coremetadata.AnnotationAgentPersona:       "go-reviewer",
			coremetadata.AnnotationAgentPersonaDigest: persona.Digest([]byte("a snapshot that was never written")),
			coremetadata.AnnotationAgentEffort:        effortInvalidFixture,
		}
		split := newPickerLaunchValuesFixture(t, nil)
		split.hold(t, "agt-beta-codex", aiModeClaude, personaResumeConversation, bundle, nil)
		window := newPickerLaunchValuesFixture(t, split.planner)
		window.hold(t, "agt-beta-codex", aiModeClaude, personaResumeConversation, bundle, nil)

		splitLine := oneClientLine(t, "split funnel", split.splitFunnelClientLines(t, client, pickerAnswer(personaResumeConversation)))
		windowLine := oneClientLine(t, "Window funnel", window.windowFunnelClientLines(t, client, pickerAnswer(personaResumeConversation)))

		disclosureCarriedBy(t, "split funnel", splitLine, "projmux: ")
		disclosureCarriedBy(t, "Window funnel", windowLine, windowCreatedMessage+": ")
		for funnel, line := range map[string]string{"split funnel": splitLine, "Window funnel": windowLine} {
			for _, reason := range []string{persona.ReasonUnavailable, claudeEffortReasonInvalid} {
				if got := strings.Count(line, reason); got != 1 {
					t.Fatalf("%s client line = %q carries %s %d times, want exactly once", funnel, line, reason, got)
				}
			}
		}
	})
}

// TestWindowCreateMoveFailureKeepsBothTheReasonAndTheDisclosure pins the other
// committed exit of the Window route. The Window and its Agent are durable and
// the pressing client could not be moved onto them, so the one line it gets
// must say why it did not move *and* still carry what the create could not
// carry over: the move failure is not a reason to drop the disclosure.
func TestWindowCreateMoveFailureKeepsBothTheReasonAndTheDisclosure(t *testing.T) {
	t.Parallel()
	const client = "/dev/pts/9"
	const failure = "injected switch-client failure"
	bundle := claudeLaunchBundle(map[string]string{
		coremetadata.AnnotationAgentPersona: "go-reviewer", coremetadata.AnnotationAgentPersonaDigest: "sha256:0",
	}, "low")
	f := newPickerLaunchValuesFixture(t, nil)
	f.hold(t, "agt-alpha-codex", aiModeClaude, personaResumeConversation, bundle, nil)
	f.hold(t, "agt-beta-codex", aiModeClaude, personaResumeConversation, claudeLaunchBundle(bundle, "high"), nil)
	f.tmux.fail = []string{"switch-client"}
	f.tmux.failMessage = failure

	line := oneClientLine(t, "Window funnel", f.windowFunnelClientLines(t, client, pickerAnswer(personaResumeConversation)))

	if !strings.HasPrefix(line, windowCreatedUnshownMessage) {
		t.Fatalf("client line = %q, want it to lead with %q", line, windowCreatedUnshownMessage)
	}
	for _, want := range []string{failure, launchValuesReasonAmbiguous} {
		if !strings.Contains(line, want) {
			t.Fatalf("client line = %q, want it to carry %q", line, want)
		}
	}
	for _, banned := range []string{"rolled back", "nothing was created", "projmux: projmux:"} {
		if strings.Contains(line, banned) {
			t.Fatalf("client line = %q must not claim %q", line, banned)
		}
	}
}

// TestWindowCreateWithNothingToDiscloseShowsTheSuccessLineUnchanged is the
// regression half. A create that carried everything over has nothing to say,
// and the line the operator reads is the bounded success constant, byte for
// byte, exactly as it was before this consumer learned to carry notices.
//
// The inheriting row is also the detector Epic 120's live measurement asks for:
// a resume that inherits a persona it can repass must not start warning
// `persona-unavailable`. That warning appearing here would be a real defect,
// not a restored disclosure.
func TestWindowCreateWithNothingToDiscloseShowsTheSuccessLineUnchanged(t *testing.T) {
	t.Parallel()
	const client = "/dev/pts/9"
	for _, test := range []struct {
		name string
		hold func(t *testing.T, f pickerLaunchValuesFixture)
	}{
		{name: "no Agent holds the conversation"},
		{name: "one holder whose bundle the create repasses whole", hold: func(t *testing.T, f pickerLaunchValuesFixture) {
			personaAnnotations, _ := createPersonaForResume(t, f.planner, "go-reviewer", []byte(personaResumeContent))
			f.hold(t, "agt-beta-codex", aiModeClaude, personaResumeConversation, claudeLaunchBundle(personaAnnotations, "low"), nil)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			f := newPickerLaunchValuesFixture(t, nil)
			if test.hold != nil {
				test.hold(t, f)
			}

			line := oneClientLine(t, "Window funnel", f.windowFunnelClientLines(t, client, pickerAnswer(personaResumeConversation)))

			if line != windowCreatedMessage {
				t.Fatalf("client line = %q, want the success constant %q unchanged", line, windowCreatedMessage)
			}
			for _, reason := range []string{persona.ReasonUnavailable, launchValuesReasonAmbiguous, claudeEffortReasonInvalid} {
				if strings.Contains(line, reason) {
					t.Fatalf("client line = %q warns %s with nothing to disclose", line, reason)
				}
			}
		})
	}
}

// TestCommittedWindowIntentDetailStaysTheFailureHalvesParameter pins the
// blast radius of this change to the Window create. finishWindowIntent's commit
// branch is shared: the Window rename, the Pane rename and the Window delete
// all hand it their canonical route's stderr as detail alongside a nil error,
// and it has always ignored it there. A committed intent that wants to
// disclose something puts it in the success line it hands over, the way
// windowCreatedLine does, so those three surfaces keep the line they had.
func TestCommittedWindowIntentDetailStaysTheFailureHalvesParameter(t *testing.T) {
	t.Parallel()
	const client = "/dev/pts/9"
	tmux := &fakeTmux{}
	cmd := &tmuxCommand{runner: tmux}
	for _, label := range []string{"Rename Window", "Rename Pane", "Delete Window"} {
		if err := cmd.finishWindowIntent(client, label, "projmux "+label+" committed", "a canonical route notice", nil); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
	}
	for i, message := range tmux.clientMessages {
		if strings.Contains(message.text, "a canonical route notice") {
			t.Fatalf("committed line %d = %q carries the detail the failure half owns", i, message.text)
		}
	}
	if len(tmux.clientMessages) != 3 {
		t.Fatalf("committed lines = %+v, want one per intent", tmux.clientMessages)
	}
}

// TestWindowCreatedLineIsTheConstantWithNothingToDisclose pins the one rule
// the eight existing assertions on windowCreatedMessage depend on.
func TestWindowCreatedLineIsTheConstantWithNothingToDisclose(t *testing.T) {
	t.Parallel()
	for _, notice := range []string{"", "   ", "\n", " \n\t "} {
		if got := windowCreatedLine(notice); got != windowCreatedMessage {
			t.Fatalf("windowCreatedLine(%q) = %q, want the constant %q", notice, got, windowCreatedMessage)
		}
	}
	if got, want := windowCreatedLine(" a notice "), windowCreatedMessage+": a notice"; got != want {
		t.Fatalf("windowCreatedLine carried the notice as %q, want %q", got, want)
	}
}
