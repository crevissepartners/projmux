package app

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// window_create_launch_default_test.go covers applyLaunchDefault -- the saved
// launch default opened on an already-committed shell Pane, which is how a
// fresh Project open still fills its first Window -- and the replace-origin
// picker transport that path uses. A generated Window create asks before it
// commits instead (window_create_launch_choice_test.go).

const (
	launchDefaultOriginPane = "%41"
	launchDefaultClient     = "/dev/pts/2"
)

// replaceRecorder is the create and delete pair of a replacing producer,
// recorded in the order they were called. Order is the contract: an Agent is
// committed before the shell it replaces is deleted, so a Window is never left
// without a Pane.
type replaceRecorder struct {
	intents   []agentPaneIntent
	created   createdPaneRuntime
	createErr error
	deleted   []string
	deleteErr error
	events    []string
}

func (r *replaceRecorder) createFromIntent(intent agentPaneIntent, _, _ io.Writer) (createdPaneRuntime, error) {
	r.intents = append(r.intents, intent)
	r.events = append(r.events, "create")
	return r.created, r.createErr
}

func (r *replaceRecorder) deletePane(paneID string, _, _ io.Writer) error {
	r.deleted = append(r.deleted, paneID)
	r.events = append(r.events, "delete")
	return r.deleteErr
}

// launchDefaultAICommand is the AI command a Window producer reaches: a
// recorded canonical create, a recorded canonical Pane delete, and a temporary
// home holding the saved mode file.
func launchDefaultAICommand(t *testing.T, home string) (*aiCommand, *replaceRecorder) {
	t.Helper()
	cmd := testAICommand(home)
	recorder := &replaceRecorder{}
	cmd.panes = recorder
	cmd.paneDelete = recorder.deletePane
	return cmd, recorder
}

// TestApplyLaunchDefaultOpensTheSavedModeOnACommittedShellPane is the condition
// table of the applied default. Every saved mode is a row, including the unset
// file, and each row states what reached the canonical routes.
func TestApplyLaunchDefaultOpensTheSavedModeOnACommittedShellPane(t *testing.T) {
	for _, tt := range []struct {
		name       string
		mode       string
		unset      bool
		wantIntent *agentPaneIntent
		wantDelete bool
		wantPopup  []string
	}{
		{name: "shell", mode: aiModeShell},
		{
			name: "claude", mode: aiModeClaude, wantDelete: true,
			wantIntent: &agentPaneIntent{
				producer: canonicalProducerSavedDefault, provider: aiModeClaude, placement: "right",
				anchorPaneID: launchDefaultOriginPane, targetClient: launchDefaultClient,
			},
		},
		{
			name: "codex", mode: aiModeCodex, wantDelete: true,
			wantIntent: &agentPaneIntent{
				producer: canonicalProducerSavedDefault, provider: aiModeCodex, placement: "right",
				anchorPaneID: launchDefaultOriginPane, targetClient: launchDefaultClient,
			},
		},
		{
			name: "antigravity", mode: aiModeAntigravity, wantDelete: true,
			wantIntent: &agentPaneIntent{
				producer: canonicalProducerSavedDefault, provider: aiModeAntigravity, placement: "right",
				anchorPaneID: launchDefaultOriginPane, targetClient: launchDefaultClient,
			},
		},
		{
			name: "selective", mode: aiModeSelective,
			wantPopup: []string{"internal", "tmux", "popup-toggle", "--client", launchDefaultClient,
				"--anchor", launchDefaultOriginPane, popupToggleReplaceOriginFlag, "ai-split-picker-right"},
		},
		{
			name: "resume", mode: aiModeResume,
			wantPopup: []string{"internal", "tmux", "popup-toggle", "--client", launchDefaultClient,
				"--anchor", launchDefaultOriginPane, popupToggleReplaceOriginFlag, "ai-split-resume-right"},
		},
		{
			name: "unset", unset: true,
			wantPopup: []string{"internal", "tmux", "popup-toggle", "--client", launchDefaultClient,
				"--anchor", launchDefaultOriginPane, popupToggleReplaceOriginFlag, "ai-split-picker-right"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			cmd, recorder := launchDefaultAICommand(t, home)
			if !tt.unset {
				if err := cmd.setMode(tt.mode); err != nil {
					t.Fatalf("setMode(%s) error = %v", tt.mode, err)
				}
			}
			cmdRecorder(cmd).commands = nil

			result := cmd.applyLaunchDefault(launchDefaultOriginPane, launchDefaultClient)

			if result.problem != "" {
				t.Fatalf("problem = %q, want none", result.problem)
			}
			var wantIntents []agentPaneIntent
			if tt.wantIntent != nil {
				wantIntents = []agentPaneIntent{*tt.wantIntent}
			}
			if !reflect.DeepEqual(recorder.intents, wantIntents) {
				t.Fatalf("create intents = %+v, want %+v", recorder.intents, wantIntents)
			}
			if tt.wantDelete {
				if !slices.Equal(recorder.deleted, []string{launchDefaultOriginPane}) {
					t.Fatalf("deleted Panes = %v, want exactly the new shell %s", recorder.deleted, launchDefaultOriginPane)
				}
				if !slices.Equal(recorder.events, []string{"create", "delete"}) {
					t.Fatalf("route order = %v, want the Agent committed before the shell is deleted", recorder.events)
				}
			} else if len(recorder.deleted) != 0 {
				t.Fatalf("deleted Panes = %v, want none", recorder.deleted)
			}
			if tt.wantPopup == nil {
				if result.picker {
					t.Fatal("a non-picker mode reported that a picker owns the result")
				}
				if len(cmdRecorder(cmd).commands) != 0 {
					t.Fatalf("commands = %#v, want none; the canonical routes own every mutation",
						cmdRecorder(cmd).commands)
				}
				return
			}
			if !result.picker {
				t.Fatal("a picker mode did not report that the popup owns the result")
			}
			if !containsAICommandArgs(cmdRecorder(cmd).commands, "/tmp/projmux", tt.wantPopup) {
				t.Fatalf("commands = %#v, want the marked popup toggle %v", cmdRecorder(cmd).commands, tt.wantPopup)
			}
		})
	}
}

// TestApplyLaunchDefaultFailuresKeepTheShellAndSayOneThing is the negative half.
// None of these roll anything back: the Window and whatever Panes exist when
// the failure happens stay, and the producer gets exactly one line to show.
func TestApplyLaunchDefaultFailuresKeepTheShellAndSayOneThing(t *testing.T) {
	t.Run("provider disabled in Settings", func(t *testing.T) {
		home := t.TempDir()
		enableAgents(t, home, config.AIAgentClaude)
		cmd, recorder := launchDefaultAICommand(t, home)
		if err := cmd.setMode(aiModeCodex); err != nil {
			t.Fatalf("setMode(codex) error = %v", err)
		}
		cmdRecorder(cmd).commands = nil

		result := cmd.applyLaunchDefault(launchDefaultOriginPane, launchDefaultClient)

		for _, want := range []string{"AI split default codex is disabled", "keeps its shell Pane"} {
			if !strings.Contains(result.problem, want) {
				t.Fatalf("problem = %q, want substring %q", result.problem, want)
			}
		}
		if len(recorder.intents) != 0 || len(recorder.deleted) != 0 {
			t.Fatalf("a disabled provider reached the canonical routes: intents=%+v deleted=%v",
				recorder.intents, recorder.deleted)
		}
		if len(cmdRecorder(cmd).commands) != 0 {
			t.Fatalf("commands = %#v, want none", cmdRecorder(cmd).commands)
		}
	})

	t.Run("Agent create refused", func(t *testing.T) {
		home := t.TempDir()
		cmd, recorder := launchDefaultAICommand(t, home)
		recorder.createErr = errors.New("injected canonical create refusal")
		if err := cmd.setMode(aiModeClaude); err != nil {
			t.Fatalf("setMode(claude) error = %v", err)
		}

		result := cmd.applyLaunchDefault(launchDefaultOriginPane, launchDefaultClient)

		for _, want := range []string{"injected canonical create refusal", "keeps its shell Pane"} {
			if !strings.Contains(result.problem, want) {
				t.Fatalf("problem = %q, want substring %q", result.problem, want)
			}
		}
		if len(recorder.deleted) != 0 {
			t.Fatalf("a refused create still deleted %v", recorder.deleted)
		}
	})

	t.Run("shell delete refused", func(t *testing.T) {
		home := t.TempDir()
		cmd, recorder := launchDefaultAICommand(t, home)
		recorder.deleteErr = errors.New("injected canonical delete refusal")
		if err := cmd.setMode(aiModeClaude); err != nil {
			t.Fatalf("setMode(claude) error = %v", err)
		}

		result := cmd.applyLaunchDefault(launchDefaultOriginPane, launchDefaultClient)

		for _, want := range []string{launchDefaultOriginPane, "injected canonical delete refusal", "both Panes stay"} {
			if !strings.Contains(result.problem, want) {
				t.Fatalf("problem = %q, want substring %q", result.problem, want)
			}
		}
		if len(recorder.intents) != 1 {
			t.Fatalf("create intents = %+v, want the one committed Agent", recorder.intents)
		}
		for _, banned := range []string{"rolled back", "reverted", "nothing was created"} {
			if strings.Contains(result.problem, banned) {
				t.Fatalf("problem = %q, must not claim %q", result.problem, banned)
			}
		}
	})
}

// TestCanonicalWindowCreateCarriesTheCommittedShellPane runs the real canonical
// Window create over the fake server and proves the runtime placement it
// returns names the shell Pane the transaction committed -- the value seam the
// answer is filled into, with no stdout parsing and no Pane-order guessing.
func TestCanonicalWindowCreateCarriesTheCommittedShellPane(t *testing.T) {
	route := newWindowCreateIntentRoute(t, false, true)
	before := route.windowUIDs()
	origins := [][2]string{}
	route.cmd.launchApply = func(originPaneID, client string, _ launchChoice) launchDefaultResult {
		origins = append(origins, [2]string{originPaneID, client})
		return launchDefaultResult{}
	}

	if err := route.run(); err != nil {
		t.Fatalf("window-create route: %v", err)
	}
	created := route.keptWindow(t, before)
	panes := route.store.registry.PanesOf(created.Metadata.UID)
	if len(panes) != 1 {
		t.Fatalf("created Window Panes = %+v, want its one initial Pane", panes)
	}
	want := [][2]string{{livePaneWithUID(t, route.tmux, panes[0].Metadata.UID), windowCreatePressingClient}}
	if !reflect.DeepEqual(origins, want) {
		t.Fatalf("answer filled into %v, want the committed shell Pane %v", origins, want)
	}
}

// replacingPickerAICommand is the split UI running inside a popup a Window
// producer marked: the origin Pane and the exact client arrive as env, as they
// always do, plus the replacement marker.
func replacingPickerAICommand(t *testing.T, marked bool) (*aiCommand, *replaceRecorder) {
	t.Helper()
	home := t.TempDir()
	cmd, recorder := launchDefaultAICommand(t, home)
	env := map[string]string{
		"HOME":                         home,
		"TMUX":                         "/tmp/tmux-1000/projmux,7,0",
		"TMUX_SPLIT_TARGET_PANE":       launchDefaultOriginPane,
		canonicalCreateTargetClientEnv: launchDefaultClient,
	}
	if marked {
		env[splitReplaceOriginEnv] = "1"
	}
	cmd.lookupEnv = func(key string) string { return env[key] }
	return cmd, recorder
}

// TestMarkedPickerSelectionReplacesTheOriginShell is the picker popup's half of
// the table. The marker is answered at the one funnel -- reached through the
// selection continuation the popup hands off -- so a provider row replaces the
// origin shell, the shell row keeps it instead of opening a second one, a
// cancelled picker changes nothing, and an unmarked popup is today's split with
// no delete at all.
func TestMarkedPickerSelectionReplacesTheOriginShell(t *testing.T) {
	for _, tt := range []struct {
		name       string
		marked     bool
		selection  string
		wantIntent *agentPaneIntent
		wantDelete bool
	}{
		{
			name: "marked provider row", marked: true, selection: aiModeClaude, wantDelete: true,
			wantIntent: &agentPaneIntent{
				producer: canonicalProducerProviderPicker, provider: aiModeClaude, placement: "right",
				anchorPaneID: launchDefaultOriginPane, targetClient: launchDefaultClient,
			},
		},
		{name: "marked shell row", marked: true, selection: aiModeShell},
		{name: "marked cancel", marked: true},
		{
			name: "unmarked provider row", selection: aiModeClaude,
			wantIntent: &agentPaneIntent{
				producer: canonicalProducerProviderPicker, provider: aiModeClaude, placement: "right",
				anchorPaneID: launchDefaultOriginPane, targetClient: launchDefaultClient,
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmd, recorder := replacingPickerAICommand(t, tt.marked)
			stubAIPickerSelection(cmd, tt.selection)

			if err := runSplitPickerThroughContinuation(t, cmd, func() error {
				return cmd.Run([]string{"picker", "--inside", "right"}, &bytes.Buffer{}, &bytes.Buffer{})
			}); err != nil {
				t.Fatalf("picker error = %v", err)
			}
			var wantIntents []agentPaneIntent
			if tt.wantIntent != nil {
				wantIntents = []agentPaneIntent{*tt.wantIntent}
			}
			if !reflect.DeepEqual(recorder.intents, wantIntents) {
				t.Fatalf("create intents = %+v, want %+v", recorder.intents, wantIntents)
			}
			var wantDeleted []string
			if tt.wantDelete {
				wantDeleted = []string{launchDefaultOriginPane}
			}
			if !slices.Equal(recorder.deleted, wantDeleted) {
				t.Fatalf("deleted Panes = %v, want %v", recorder.deleted, wantDeleted)
			}
		})
	}
}

// TestMarkedResumeSelectionReplacesTheOriginShell covers the resume lane of the
// same funnel: a resume row is an Agent like any other, so it replaces the
// origin shell too.
func TestMarkedResumeSelectionReplacesTheOriginShell(t *testing.T) {
	cmd, recorder := replacingPickerAICommand(t, true)

	if err := runSplitPickerThroughContinuation(t, cmd, func() error {
		return cmd.runSelectedResumeSession(aiResumeSelection{agent: aiModeClaude, resumeID: "conv-1"}, "right")
	}); err != nil {
		t.Fatalf("resume selection error = %v", err)
	}
	want := []agentPaneIntent{{
		producer: canonicalProducerResumePicker, provider: aiModeClaude, placement: "right",
		conversationID: "conv-1", anchorPaneID: launchDefaultOriginPane, targetClient: launchDefaultClient,
	}}
	if !reflect.DeepEqual(recorder.intents, want) {
		t.Fatalf("create intents = %+v, want %+v", recorder.intents, want)
	}
	if !slices.Equal(recorder.events, []string{"create", "delete"}) {
		t.Fatalf("route order = %v, want the Agent committed before the shell is deleted", recorder.events)
	}
}

// TestPublicCreateWindowNeverReadsTheSavedLaunchDefault is the boundary this
// whole feature sits behind: the saved mode belongs to the UI producers, and a
// typed `projmux create window` still makes the one shell Pane it always made.
//
// The probe is the mode file itself. It is a FIFO with no writer, so any
// process that opens it blocks forever instead of reading a mode; the create
// below returns, which is the evidence that nothing on that route consulted it.
func TestPublicCreateWindowNeverReadsTheSavedLaunchDefault(t *testing.T) {
	configHome := t.TempDir()
	modeDir := filepath.Join(configHome, "projmux")
	if err := os.MkdirAll(modeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	modeFile := filepath.Join(modeDir, "tmux-ai-split-mode")
	if err := syscall.Mkfifo(modeFile, 0o600); err != nil {
		t.Skipf("the mode-file read probe needs a FIFO: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("HOME", configHome)

	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, _ := newTestResourceCreateCommand(t, store, tmux)

	if _, _, err := runRoute(t, create, "window", "--project", "beta"); err != nil {
		t.Fatalf("create window error = %v", err)
	}

	windows := store.registry.WindowsOf("prj-beta")
	created := windows[len(windows)-1]
	panes := store.registry.PanesOf(created.Metadata.UID)
	if len(panes) != 1 || panes[0].Spec.Role != coremetadata.PaneRoleShell {
		t.Fatalf("created Window Panes = %+v, want exactly one shell Pane", panes)
	}
	if len(store.registry.AgentsOf(created.Metadata.UID)) != 0 {
		t.Fatalf("typed create window opened an Agent: %+v", store.registry.AgentsOf(created.Metadata.UID))
	}
	if _, _, live := tmux.pane(livePaneWithUID(t, tmux, panes[0].Metadata.UID)); live == nil {
		t.Fatalf("the created shell Pane has no live mirror; tmux:\n%s", tmux.state())
	}
}

// TestMarkedPopupToggleCarriesTheReplacementIntentAndTheExactClient is the
// transport half of the marker: `popup-toggle` accepts the private flag only
// from a producer that named both the Pane it replaces and the client that sees
// the result, hands the popup the marker beside the origin it already carried,
// and pins the popup to that exact client.
func TestMarkedPopupToggleCarriesTheReplacementIntentAndTheExactClient(t *testing.T) {
	popupContext := tmuxPopupContext{
		OriginPane: launchDefaultOriginPane, TargetClient: launchDefaultClient,
		OriginSession: "alpha", ContextDir: "/srv/alpha", ClientWidth: 200, ClientHeight: 50,
	}
	for _, tt := range []struct {
		name   string
		args   []string
		marked bool
	}{
		{
			name: "marked split picker", marked: true,
			args: []string{"--client", launchDefaultClient, "--anchor", launchDefaultOriginPane,
				popupToggleReplaceOriginFlag, "ai-split-picker-right"},
		},
		{
			name: "marked resume picker", marked: true,
			args: []string{"--client", launchDefaultClient, "--anchor", launchDefaultOriginPane,
				popupToggleReplaceOriginFlag, "ai-split-resume-down"},
		},
		{
			name: "unmarked split picker",
			args: []string{"--client", launchDefaultClient, "--anchor", launchDefaultOriginPane, "ai-split-picker-right"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mode, err := parseTmuxPopupToggleArgs(tt.args, io.Discard)
			if err != nil {
				t.Fatalf("parse popup-toggle %v: %v", tt.args, err)
			}
			if mode.ReplaceOrigin != tt.marked {
				t.Fatalf("ReplaceOrigin = %v, want %v", mode.ReplaceOrigin, tt.marked)
			}
			command, options, err := buildPopupToggle(mode, "/tmp/projmux", "/tmp/marker", popupContext)
			if err != nil {
				t.Fatalf("buildPopupToggle() error = %v", err)
			}
			marker := splitReplaceOriginEnv + "='1'"
			if got := strings.Contains(command, marker); got != tt.marked {
				t.Fatalf("popup command = %q, want marker %q present = %v", command, marker, tt.marked)
			}
			wantClient := ""
			if tt.marked {
				wantClient = launchDefaultClient
			}
			if options.Client != wantClient {
				t.Fatalf("popup client = %q, want %q", options.Client, wantClient)
			}
			if !strings.Contains(command, "TMUX_SPLIT_TARGET_PANE='"+launchDefaultOriginPane+"'") {
				t.Fatalf("popup command = %q, want the origin Pane it always carried", command)
			}
		})
	}
}

// TestPopupToggleRefusesAnUnroutableReplacement keeps the private flag from
// meaning anything on its own: without the Pane it replaces, without the client
// that sees the result, or on a popup that opens no split picker, it refuses
// instead of opening a popup that would delete something unnamed.
func TestPopupToggleRefusesAnUnroutableReplacement(t *testing.T) {
	for _, args := range [][]string{
		{"--client", launchDefaultClient, popupToggleReplaceOriginFlag, "ai-split-picker-right"},
		{"--anchor", launchDefaultOriginPane, popupToggleReplaceOriginFlag, "ai-split-picker-right"},
		{"--client", launchDefaultClient, "--anchor", launchDefaultOriginPane, popupToggleReplaceOriginFlag, "sessionizer"},
	} {
		if _, err := parseTmuxPopupToggleArgs(args, io.Discard); err == nil {
			t.Fatalf("popup-toggle accepted %v", args)
		}
	}
}
