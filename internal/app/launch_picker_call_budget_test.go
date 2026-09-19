package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

// The tmux call budget of opening the launch picker.
//
// The new-Window key asks through `popup-toggle --client <tty> --anchor %N
// --answer <file> ai-split-picker-right`, and the split key enters the same
// route without --answer. Every call up to and including the one
// `display-popup` stands between the key press and the picker's first frame.
//
// Caps are pinned at the fake-runner measurement. A change that adds a tmux
// call before the popup opens fails here with the count, the cap, the overage,
// and a per-command breakdown.
//
// Before the anchored fast path the same route issued 14 calls: 7 ambient
// popupContext reads (#{client_tty}, the statusbar decoration, #{pane_id}, #S,
// #{pane_current_path}, #{client_width}, #{client_height}), the 5 anchor proof
// reads, the containment read, and display-popup. The fast path keeps the
// proof and folds the client geometry into the containment read.
const (
	// launchPickerPopupCallBudget is the whole popup-toggle up to display-popup:
	// 5 anchor proof reads, 1 containment-and-geometry read, 1 display-popup.
	launchPickerPopupCallBudget = 7
	// launchPickerProofCalls is the anchor proof alone, which this budget never
	// trades away.
	launchPickerProofCalls = 5
)

const (
	launchBudgetSocket = "/tmp/projmux-launch-budget/app.sock"
	launchBudgetName   = "projmux-launch-budget"
	launchBudgetPID    = "4242"
	launchBudgetAnchor = "%9"
)

var launchBudgetIdentityFormat = tmuxRowFormat("#{socket_path}", "#{pid}", "#{session_id}", "#{window_id}", "#{pane_id}")

var launchBudgetCombinedFormat = tmuxRowFormat("#{pane_id}", "#S", "#{pane_current_path}", "#{client_width}", "#{client_height}")

var launchBudgetContainmentFormat = tmuxRowFormat("#{pane_id}", "#S", "#{pane_current_path}")

// launchBudgetRow joins one tmux row the way the formats above render.
func launchBudgetRow(fields ...string) string {
	return strings.Join(fields, tmuxRowSepFormat) + "\n"
}

// launchBudgetCombinedKey is the fast path's one containment-and-geometry read.
func launchBudgetCombinedKey(client string) string {
	return recordedTmuxCallKey("tmux", "-S", launchBudgetSocket, "display-message", "-p", "-c", client,
		"-t", launchBudgetAnchor, "-F", launchBudgetCombinedFormat)
}

// launchBudgetContainmentKey is the plain containment read of the old path.
func launchBudgetContainmentKey() string {
	return recordedTmuxCallKey("tmux", "-S", launchBudgetSocket, "display-message", "-p",
		"-t", launchBudgetAnchor, "-F", launchBudgetContainmentFormat)
}

// newLaunchBudgetPopupToggle builds a popup-toggle invoked from a key binding
// of the app-owned server: TMUX names the exact socket and server PID, the
// proof reads answer for that server, and the anchor Pane is %9 in session
// alpha. The ambient formats answer too, so a fallback to the old reads is
// observable rather than a crash.
func newLaunchBudgetPopupToggle(t *testing.T, client string) (*tmuxCommand, *recordingTmuxRunner) {
	t.Helper()
	marker := popupMarkerPath(sanitizePopupKey(client), "ai-split-picker")
	_ = os.Remove(marker)
	t.Cleanup(func() { _ = os.Remove(marker) })
	runner := &recordingTmuxRunner{
		formats: map[string]string{
			"#{client_tty}":        client,
			"#{pane_id}":           "%ambient",
			"#S":                   "ambient",
			"#{pane_current_path}": "/srv/ambient",
			"#{client_width}":      "200",
			"#{client_height}":     "50",
		},
		outputs: map[string]string{
			recordedTmuxCallKey("tmux", "-S", launchBudgetSocket, "show-options", "-gqv", runtimeMutationSocketNameOption): launchBudgetName + "\n",
			recordedTmuxCallKey("tmux", "-L", launchBudgetName, "display-message", "-p", "-F", "#{socket_path}"):           launchBudgetSocket + "\n",
			recordedTmuxCallKey("tmux", "-S", launchBudgetSocket, "display-message", "-p", "-t", launchBudgetAnchor, "-F", launchBudgetIdentityFormat): launchBudgetRow(
				launchBudgetSocket, launchBudgetPID, "$1", "@2", launchBudgetAnchor),
			launchBudgetCombinedKey(client): launchBudgetRow(launchBudgetAnchor, "alpha", "/srv/alpha", "200", "50"),
			launchBudgetContainmentKey():    launchBudgetRow(launchBudgetAnchor, "alpha", "/srv/alpha"),
		},
	}
	home := t.TempDir()
	cmd := &tmuxCommand{
		runner:     runner,
		executable: func() (string, error) { return "/tmp/projmux", nil },
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv: func(key string) string {
			if key == "TMUX" {
				return launchBudgetSocket + "," + launchBudgetPID + ",0"
			}
			return ""
		},
	}
	return cmd, runner
}

// launchBudgetArgs is the popup-toggle argv of the new-Window key (answer
// mode) or of the split key.
func launchBudgetArgs(t *testing.T, client string, answer bool) []string {
	args := []string{"popup-toggle", "--client", client, "--anchor", launchBudgetAnchor}
	if answer {
		args = append(args, popupToggleAnswerFlag, filepath.Join(t.TempDir(), "answer.json"))
	}
	return append(args, "ai-split-picker-right")
}

// launchBudgetCallsUntilPopup is every tmux call up to and including the
// first display-popup, and whether display-popup was reached.
func launchBudgetCallsUntilPopup(calls []recordedTmuxCall) ([]recordedTmuxCall, bool) {
	for i, call := range calls {
		if call.name == "tmux" && slices.Contains(tmuxCommandArgv(call.args), "display-popup") {
			return calls[:i+1], true
		}
	}
	return calls, false
}

// launchBudgetBreakdown renders one line per distinct command, with its count.
func launchBudgetBreakdown(calls []recordedTmuxCall) string {
	counts := map[string]int{}
	for _, call := range calls {
		argv := tmuxCommandArgv(call.args)
		key := strings.Join(call.args, " ")
		if len(argv) > 0 && argv[0] == "display-popup" {
			key = "display-popup"
		}
		counts[strings.ReplaceAll(key, tmuxRowSepFormat, "|")]++
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&b, "\n  %3d  %s", counts[key], key)
	}
	return b.String()
}

func launchBudgetHasCall(calls []recordedTmuxCall, key string) bool {
	for _, call := range calls {
		if recordedTmuxCallKey(call.name, call.args...) == key {
			return true
		}
	}
	return false
}

// launchBudgetAmbientReads counts the unrouted popupContext reads.
func launchBudgetAmbientReads(calls []recordedTmuxCall) int {
	n := 0
	for _, call := range calls {
		if call.name == "tmux" && len(call.args) == 4 && slices.Equal(call.args[:3], []string{"display-message", "-p", "-F"}) {
			n++
		}
	}
	return n
}

func launchBudgetDisplayPopup(calls []recordedTmuxCall) []string {
	for _, call := range calls {
		if argv := tmuxCommandArgv(call.args); len(argv) > 0 && argv[0] == "display-popup" {
			return argv
		}
	}
	return nil
}

// TestLaunchPickerPopupStaysWithinItsTmuxCallBudget pins the tmux calls between
// the new-Window key (answer mode) or the split key and the picker popup.
func TestLaunchPickerPopupStaysWithinItsTmuxCallBudget(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name   string
		client string
		answer bool
	}{
		{name: "new window answer mode", client: "/dev/pts/launch-budget-answer", answer: true},
		{name: "split key", client: "/dev/pts/launch-budget-split"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cmd, runner := newLaunchBudgetPopupToggle(t, tt.client)

			if err := cmd.Run(launchBudgetArgs(t, tt.client, tt.answer), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("popup-toggle error = %v", err)
			}

			calls, opened := launchBudgetCallsUntilPopup(runner.calls)
			if !opened {
				t.Fatalf("popup-toggle never opened the popup:%s", launchBudgetBreakdown(runner.calls))
			}
			if got := len(calls); got > launchPickerPopupCallBudget {
				t.Fatalf("launch picker popup issued %d tmux calls, cap %d, over by %d:%s",
					got, launchPickerPopupCallBudget, got-launchPickerPopupCallBudget, launchBudgetBreakdown(calls))
			}
			if n := launchBudgetAmbientReads(calls); n != 0 {
				t.Fatalf("anchored popup for an exact client issued %d ambient reads:%s", n, launchBudgetBreakdown(calls))
			}
			if !launchBudgetHasCall(calls, launchBudgetCombinedKey(tt.client)) {
				t.Fatalf("popup-toggle did not read containment and geometry on the exact client:%s", launchBudgetBreakdown(calls))
			}
			popup := launchBudgetDisplayPopup(calls)
			if !containsTmuxArgPair(popup, "-t", launchBudgetAnchor) {
				t.Fatalf("display-popup = %q, want the exact anchor", popup)
			}
			// Answer mode names the pressing client on display-popup; the split
			// key's popup never has.
			if tt.answer && (!containsTmuxArgPair(popup, "-c", tt.client) || !strings.Contains(strings.Join(popup, " "), splitAnswerFileEnv+"=")) {
				t.Fatalf("display-popup = %q, want the exact client and the answer file", popup)
			}
		})
	}
}

// TestLaunchPickerFastPathOpensTheSamePopupAsTheAmbientReads is the parity
// proof of the fast path: given the same observed anchor row and client
// geometry, the popup it opens is byte-for-byte the popup the ambient reads
// followed by the plain containment read open. The combined read failing is
// what forces the old sequence, which is also its fallback behavior.
func TestLaunchPickerFastPathOpensTheSamePopupAsTheAmbientReads(t *testing.T) {
	t.Parallel()

	for _, answer := range []bool{true, false} {
		t.Run(fmt.Sprintf("answer=%t", answer), func(t *testing.T) {
			t.Parallel()
			client := fmt.Sprintf("/dev/pts/launch-budget-parity-%t", answer)
			args := launchBudgetArgs(t, client, answer)

			fast, fastRunner := newLaunchBudgetPopupToggle(t, client)
			if err := fast.Run(args, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("fast path error = %v", err)
			}
			_ = os.Remove(popupMarkerPath(sanitizePopupKey(client), "ai-split-picker"))

			slow, slowRunner := newLaunchBudgetPopupToggle(t, client)
			slowRunner.errors = map[string]error{launchBudgetCombinedKey(client): errors.New("can't find client: " + client)}
			if err := slow.Run(args, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("fallback path error = %v", err)
			}

			fastPopup, slowPopup := launchBudgetDisplayPopup(fastRunner.calls), launchBudgetDisplayPopup(slowRunner.calls)
			if fastPopup == nil || !slices.Equal(fastPopup, slowPopup) {
				t.Fatalf("fast path popup = %q\nold path popup  = %q", fastPopup, slowPopup)
			}
			slowCalls, _ := launchBudgetCallsUntilPopup(slowRunner.calls)
			if n := launchBudgetAmbientReads(slowCalls); n != 7 {
				t.Fatalf("fallback issued %d ambient reads, want today's 7:%s", n, launchBudgetBreakdown(slowCalls))
			}
			if !launchBudgetHasCall(slowCalls, launchBudgetContainmentKey()) {
				t.Fatalf("fallback skipped the plain containment read:%s", launchBudgetBreakdown(slowCalls))
			}
			// The failed combined read plus today's 14.
			if got := len(slowCalls); got != 15 {
				t.Fatalf("fallback issued %d calls, want 15:%s", got, launchBudgetBreakdown(slowCalls))
			}
		})
	}
}

// TestLaunchPickerFastPathRefusesAnchorDriftAndServerReplacement keeps the
// fast path exactly as strict as the old one: a replaced server or a drifted
// anchor is refused before any popup, and a containment row naming another
// Pane is the same drift error, not a reason to fall back.
func TestLaunchPickerFastPathRefusesAnchorDriftAndServerReplacement(t *testing.T) {
	t.Parallel()

	identityKey := recordedTmuxCallKey("tmux", "-S", launchBudgetSocket, "display-message", "-p", "-t", launchBudgetAnchor,
		"-F", launchBudgetIdentityFormat)
	for _, tt := range []struct {
		name string
		// arrange injects the drift into the runner.
		arrange func(runner *recordingTmuxRunner, client string)
		want    string
		// proofRefusal is a refusal before any containment read.
		proofRefusal bool
	}{
		{
			name: "combined row names another Pane",
			arrange: func(runner *recordingTmuxRunner, client string) {
				runner.outputs[launchBudgetCombinedKey(client)] = launchBudgetRow("%10", "alpha", "/srv/alpha", "200", "50")
			},
			want: "tmux popup-toggle exact anchor containment drifted",
		},
		{
			name: "combined read answers two rows",
			arrange: func(runner *recordingTmuxRunner, client string) {
				runner.outputs[launchBudgetCombinedKey(client)] = launchBudgetRow(launchBudgetAnchor, "alpha", "/srv/alpha", "200", "50") +
					launchBudgetRow("%10", "beta", "/srv/beta", "200", "50")
			},
			want: "tmux popup-toggle exact anchor containment drifted",
		},
		{
			name: "combined read fails and the plain row names another Pane",
			arrange: func(runner *recordingTmuxRunner, client string) {
				runner.errors = map[string]error{launchBudgetCombinedKey(client): errors.New("can't find client")}
				runner.outputs[launchBudgetContainmentKey()] = launchBudgetRow("%10", "alpha", "/srv/alpha")
			},
			want: "tmux popup-toggle exact anchor containment drifted",
		},
		{
			name: "combined read is malformed and the plain read fails",
			arrange: func(runner *recordingTmuxRunner, client string) {
				runner.outputs[launchBudgetCombinedKey(client)] = launchBudgetRow(launchBudgetAnchor, "alpha", "/srv/alpha")
				runner.errors = map[string]error{launchBudgetContainmentKey(): errors.New("can't find pane")}
			},
			want: "tmux popup-toggle exact anchor containment drifted",
		},
		{
			name: "server replaced under the same socket",
			arrange: func(runner *recordingTmuxRunner, _ string) {
				runner.outputs[identityKey] = launchBudgetRow(launchBudgetSocket, "9999", "$1", "@2", launchBudgetAnchor)
			},
			want: "tmux popup-toggle exact anchor authority", proofRefusal: true,
		},
		{
			name: "anchor answers from another socket",
			arrange: func(runner *recordingTmuxRunner, _ string) {
				runner.outputs[identityKey] = launchBudgetRow("/tmp/projmux-launch-budget/foreign.sock", launchBudgetPID, "$1", "@2", launchBudgetAnchor)
			},
			want: "tmux popup-toggle exact anchor authority", proofRefusal: true,
		},
		{
			name: "anchor Pane is gone",
			arrange: func(runner *recordingTmuxRunner, _ string) {
				runner.errors = map[string]error{identityKey: errors.New("can't find pane: " + launchBudgetAnchor)}
			},
			want: "tmux popup-toggle exact anchor authority", proofRefusal: true,
		},
		{
			name: "inherited socket drifted",
			arrange: func(runner *recordingTmuxRunner, _ string) {
				runner.outputs[recordedTmuxCallKey("tmux", "-S", launchBudgetSocket, "display-message", "-p", "-F", "#{socket_path}")] =
					"/tmp/projmux-launch-budget/other.sock\n"
			},
			want: "tmux popup-toggle exact anchor authority", proofRefusal: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := "/dev/pts/launch-budget-drift-" + strings.ReplaceAll(tt.name, " ", "-")
			for _, answer := range []bool{true, false} {
				cmd, runner := newLaunchBudgetPopupToggle(t, client)
				tt.arrange(runner, client)

				err := cmd.Run(launchBudgetArgs(t, client, answer), &bytes.Buffer{}, &bytes.Buffer{})
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("answer=%t: popup-toggle error = %v, want %q", answer, err, tt.want)
				}
				if popup := launchBudgetDisplayPopup(runner.calls); popup != nil {
					t.Fatalf("answer=%t: refused popup-toggle still opened %q", answer, popup)
				}
				if _, statErr := os.Stat(popupMarkerPath(sanitizePopupKey(client), "ai-split-picker")); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("answer=%t: refused popup-toggle left a marker (stat err %v)", answer, statErr)
				}
				if tt.proofRefusal {
					if launchBudgetHasCall(runner.calls, launchBudgetCombinedKey(client)) || launchBudgetHasCall(runner.calls, launchBudgetContainmentKey()) {
						t.Fatalf("answer=%t: proof refusal still read containment:%s", answer, launchBudgetBreakdown(runner.calls))
					}
					if n := launchBudgetAmbientReads(runner.calls); n != 0 {
						t.Fatalf("answer=%t: proof refusal issued %d ambient reads", answer, n)
					}
					if len(runner.calls) > launchPickerProofCalls {
						t.Fatalf("answer=%t: proof refusal issued %d calls, more than the %d-call proof:%s",
							answer, len(runner.calls), launchPickerProofCalls, launchBudgetBreakdown(runner.calls))
					}
				}
			}
		})
	}
}

// TestLaunchPickerQuestionAddsNoTmuxCallsBeforePopupToggle covers the
// window-create side: asking for the new Window's first Pane execs
// popup-toggle and issues no tmux call of its own before it, through either
// exec seam of the AI command.
func TestLaunchPickerQuestionAddsNoTmuxCallsBeforePopupToggle(t *testing.T) {
	claudeAnswer := agentPaneIntent{producer: canonicalProducerProviderPicker, provider: aiModeClaude, placement: "right"}
	for _, mode := range []string{aiModeSelective, aiModeResume, ""} {
		t.Run("mode="+mode, func(t *testing.T) {
			cmd, _, popups := answeringLaunchAICommand(t, mode, encodedAnswer(t, claudeAnswer), nil)
			var tmuxBeforePopup []string
			run, read := cmd.runCommand, cmd.readCommand
			cmd.runCommand = func(ctx context.Context, name string, args ...string) error {
				if name == "tmux" && len(*popups) == 0 {
					tmuxBeforePopup = append(tmuxBeforePopup, strings.Join(args, " "))
				}
				return run(ctx, name, args...)
			}
			cmd.readCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if name == "tmux" && len(*popups) == 0 {
					tmuxBeforePopup = append(tmuxBeforePopup, strings.Join(args, " "))
				}
				return read(ctx, name, args...)
			}

			got := cmd.chooseLaunchDefault(launchChoicePressedPane, launchDefaultClient)

			if len(*popups) != 1 || !reflect.DeepEqual(got, launchChoice{intent: claudeAnswer}) {
				t.Fatalf("choice = %+v popups = %v, want one answered popup", got, *popups)
			}
			if len(tmuxBeforePopup) != 0 {
				t.Fatalf("asking issued %d tmux calls before popup-toggle: %q", len(tmuxBeforePopup), tmuxBeforePopup)
			}
		})
	}
}

// TestAgentPickerIssuesNoTmuxCallsBeforeItsFirstFrame covers the picker side:
// the popup's `agent-pane picker --inside` renders its rows without any tmux
// call through the AI command's exec seams.
func TestAgentPickerIssuesNoTmuxCallsBeforeItsFirstFrame(t *testing.T) {
	cmd := testAICommand(t.TempDir())
	picker := &capturingAIRunner{}
	cmd.nativePicker = nativePickerFromCompatRunner(picker)
	var tmuxCalls []string
	run, read := cmd.runCommand, cmd.readCommand
	cmd.runCommand = func(ctx context.Context, name string, args ...string) error {
		if name == "tmux" {
			tmuxCalls = append(tmuxCalls, strings.Join(args, " "))
		}
		return run(ctx, name, args...)
	}
	cmd.readCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" {
			tmuxCalls = append(tmuxCalls, strings.Join(args, " "))
		}
		return read(ctx, name, args...)
	}

	if err := cmd.runPicker([]string{"--inside", "right"}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runPicker() error = %v", err)
	}
	if len(picker.options.Entries) == 0 {
		t.Fatal("picker never rendered its rows")
	}
	if len(tmuxCalls) != 0 {
		t.Fatalf("picker issued %d tmux calls before its first frame: %q", len(tmuxCalls), tmuxCalls)
	}
}
