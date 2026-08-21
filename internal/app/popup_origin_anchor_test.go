package app

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"

	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// popup_origin_anchor_test.go covers the split UI running where it has no
// inherited target of its own.
//
// `display-popup -E` exports $TMUX to its job and deliberately exports no
// $TMUX_PANE, because a popup is not a pane. Every terminal action of the two
// split pickers therefore reached create with nothing to derive a scope from and
// refused with the `--project` usage error, which is what took the default split
// key and both pickers out at once. The origin pane the keypress came from is the
// answer, and these tests fix both halves of it: an anchored intent resolves the
// scope, and an invocation that carries no anchor still refuses.

// popupEnv is the environment of a tmux popup job: inside a client, with no pane
// of its own.
func popupEnv(pane string) func(string) string {
	env := map[string]string{"TMUX": "/tmp/tmux-1000/projmux,7,0"}
	if pane != "" {
		env["TMUX_SPLIT_TARGET_PANE"] = pane
	}
	return func(key string) string { return env[key] }
}

// withPopupOrigin wires a create command the way a popup job's process is wired:
// the inherited lookup sees $TMUX with no $TMUX_PANE, and the anchor lookup is
// the production one over the same server.
func withPopupOrigin(create *createCommand, tmux *fakeTmux, getenv func(string) string) {
	mirror := intmetadata.NewMirror(tmux)
	create.activeTarget = tmuxActiveTargetLookup(getenv, mirror)
	create.anchorTarget = func(paneID string) activeTargetLookup {
		return anchoredActiveTargetLookup(paneID, getenv, mirror)
	}
}

// TestPopupOriginAnchorResolvesTheCreateScopeWithNoInheritedPane is the unit half
// of the slice, and it fixes both properties in one table.
//
// The anchored column is the fix: a stated origin pane resolves Project, Window
// and split anchor through the same registry identity mirror a pane-hosted
// invocation reads, with no $TMUX_PANE anywhere. The typed column is the
// boundary: the same popup runtime, spelled as the command an operator typed,
// keeps the exact refusal it has today. An anchor is something a producer hands
// to create, never something create picks up from the environment, so making the
// popup work must not make `create pane` inside a popup work.
func TestPopupOriginAnchorResolvesTheCreateScopeWithNoInheritedPane(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		insideTmux  bool
		anchor      string
		wantRefusal string
	}{
		{
			name:       "the popup's origin pane resolves the whole scope",
			insideTmux: true,
			anchor:     "origin",
		},
		{
			name:        "a popup with no origin pane has no target at all",
			insideTmux:  true,
			wantRefusal: "no exact tmux Pane",
		},
		{
			name:        "whitespace is not a pane id",
			insideTmux:  true,
			anchor:      "   ",
			wantRefusal: "no exact tmux Pane",
		},
		{
			name:        "an anchor that leaked outside a client addresses no server",
			anchor:      "origin",
			wantRefusal: "no exact tmux Pane",
		},
		{
			name:        "an anchor carrying no managed identity is not adopted",
			insideTmux:  true,
			anchor:      "%404",
			wantRefusal: "carries no " + tmuxopts.PaneUID,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store, tmux := aliveAlphaRuntime(t)
			origin := livePaneWithUID(t, tmux, "pan-alpha-zsh")
			anchor := test.anchor
			if anchor == "origin" {
				anchor = origin
			}
			env := map[string]string{}
			if test.insideTmux {
				env["TMUX"] = "/tmp/tmux-1000/projmux,7,0"
			}
			getenv := func(key string) string { return env[key] }

			create, _ := newTestResourceCreateCommand(t, store, tmux)
			withPopupOrigin(create, tmux, getenv)
			before := paneUIDsByWindow(store)
			registryBefore := store.snapshot()

			err := create.createFromIntent(
				agentPaneIntent{producer: canonicalProducerProviderPicker, placement: "right", anchorPaneID: anchor},
				&bytes.Buffer{}, &bytes.Buffer{})

			if test.wantRefusal != "" {
				if err == nil {
					t.Fatalf("the anchored intent succeeded with anchor %q", anchor)
				}
				if !IsUsageError(err) {
					t.Fatalf("error = %v, want a usage error so exit 2 keeps meaning invalid input", err)
				}
				for _, want := range []string{"nothing was created", test.wantRefusal} {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("error = %q, want it to mention %q", err, want)
					}
				}
				if store.writes != 0 || store.snapshot() != registryBefore {
					t.Fatalf("the refusal wrote to the Registry %d times", store.writes)
				}
				return
			}
			if err != nil {
				t.Fatalf("the anchored intent failed: %v", err)
			}
			added := addedPaneUIDs(before, paneUIDsByWindow(store))
			if len(added["win-alpha-main"]) != 1 {
				t.Fatalf("the origin pane's Window gained %v, want exactly one Pane; registry:\n%s",
					added["win-alpha-main"], store.snapshot())
			}
			if len(added["win-alpha-review"]) != 0 {
				t.Fatalf("the anchor fanned out to another Window: %v", added["win-alpha-review"])
			}
			assertNoClientMovement(t, tmux)
		})
	}
}

// TestTypedCreateInsideAPopupKeepsItsRefusal is acceptance criterion 4 in the
// spelling that has to keep failing.
//
// The anchor travels on the intent, so the popup runtime alone changes nothing
// for the typed verb: an operator who opens a popup and runs `projmux create
// pane --placement right` there is still an invocation with no target, and
// answering it would mean inventing a Project from the environment.
func TestTypedCreateInsideAPopupKeepsItsRefusal(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"pane", "--placement", "right"},
		{"window"},
		{"agent", "--provider", "codex"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			store, tmux := aliveAlphaRuntime(t)
			origin := livePaneWithUID(t, tmux, "pan-alpha-zsh")
			create, _ := newTestAgentCreateCommand(t, store, tmux)
			// The origin pane is present in the popup's environment and still
			// never reaches the scope: only an intent may carry an anchor.
			withPopupOrigin(create, tmux, popupEnv(origin))
			before := store.snapshot()
			callsBefore := len(tmux.calls)

			stdout, _, err := runRoute(t, create, args...)
			if err == nil {
				t.Fatalf("create %v succeeded inside a popup", args)
			}
			if !IsUsageError(err) {
				t.Fatalf("create %v error is not a usage error: %v", args, err)
			}
			if !strings.Contains(err.Error(), "this invocation is not inside a tmux client") {
				t.Fatalf("create %v error = %q, want the unchanged refusal", args, err)
			}
			if stdout != "" {
				t.Fatalf("create %v wrote %q to stdout", args, stdout)
			}
			if store.transactions != 0 || store.writes != 0 || store.snapshot() != before {
				t.Fatalf("create %v mutated the Registry", args)
			}
			if got := len(tmux.calls) - callsBefore; got != 0 {
				t.Fatalf("create %v issued %d tmux calls, want 0", args, got)
			}
		})
	}
}

// TestEveryPopupSplitActionCarriesTheOriginPane is the integration half: the four
// terminal actions of the two pickers reach create through the same anchor.
//
// They are asserted at the producer boundary because that is where the property
// lives -- one funnel attaches the anchor, so a fifth action added later cannot
// forget to.
func TestEveryPopupSplitActionCarriesTheOriginPane(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		run  func(*aiCommand) error
		want agentPaneIntent
	}{
		{
			name: "provider row",
			run: func(c *aiCommand) error {
				return c.createAgentPane(canonicalProducerProviderPicker, aiModeCodex, "right")
			},
			want: agentPaneIntent{producer: canonicalProducerProviderPicker, provider: aiModeCodex, placement: "right"},
		},
		{
			name: "shell row",
			run:  func(c *aiCommand) error { return c.createShellPane(canonicalProducerProviderPicker, "down") },
			want: agentPaneIntent{producer: canonicalProducerProviderPicker, placement: "down"},
		},
		{
			name: "resume row",
			run: func(c *aiCommand) error {
				return c.createResumedAgentPane(canonicalProducerResumePicker, aiModeClaude, "right", "conv-7")
			},
			want: agentPaneIntent{producer: canonicalProducerResumePicker, provider: aiModeClaude, placement: "right", conversationID: "conv-7"},
		},
		{
			name: "the resume picker's new row",
			run:  func(c *aiCommand) error { return c.runAgentPickerSelection("right") },
			want: agentPaneIntent{producer: canonicalProducerProviderPicker, provider: aiModeCodex, placement: "right"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			cmd, creator := intentAICommand(t, home)
			cmd.lookupEnv = popupOriginLookupEnv(home, "%46")
			stubAIPickerSelection(cmd, aiModeCodex)

			if err := test.run(cmd); err != nil {
				t.Fatalf("%s error = %v", test.name, err)
			}
			want := test.want
			want.anchorPaneID = "%46"
			if got := []agentPaneIntent{want}; !reflect.DeepEqual(creator.intents, got) {
				t.Fatalf("intents = %+v, want %+v", creator.intents, got)
			}
		})
	}
}

// TestAPaneHostedSplitStatesNoAnchor keeps the other producer runtime byte-identical.
//
// `internal agent-pane launch-default` with a provider mode runs in the pane it
// splits, so it has an inherited target and states no anchor: create resolves it
// exactly as it does for a typed invocation. The absent origin env is also the
// zero-probe case -- this producer issues no tmux command of its own.
func TestAPaneHostedSplitStatesNoAnchor(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd, creator := intentAICommand(t, home)
	if err := cmd.setMode(aiModeCodex); err != nil {
		t.Fatalf("setMode(codex) error = %v", err)
	}
	cmdRecorder(cmd).commands = nil

	if err := cmd.Run([]string{"launch-default", "right"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("launch-default right error = %v", err)
	}
	want := []agentPaneIntent{{producer: canonicalProducerSavedDefault, provider: aiModeCodex, placement: "right"}}
	if !reflect.DeepEqual(creator.intents, want) {
		t.Fatalf("intents = %+v, want %+v", creator.intents, want)
	}
	if len(cmdRecorder(cmd).commands) != 0 {
		t.Fatalf("commands = %#v, want none; a pane-hosted producer probes nothing", cmdRecorder(cmd).commands)
	}
}

// TestTheOriginPaneIsNotAGlobalScope is the excluded-range guard.
//
// $TMUX_SPLIT_TARGET_PANE is written by the popup builder and read by the split
// UI. Promoting it to a fallback the active-target seam consults would turn one
// popup's origin pane into an implicit scope override for every read, rename and
// delete verb, which is a different decision with different defaults and is
// explicitly out of this slice.
func TestTheOriginPaneIsNotAGlobalScope(t *testing.T) {
	t.Parallel()

	const key = `"TMUX_SPLIT_TARGET_PANE"`
	// tmux.go writes it into the two split-picker popup modes; ai.go writes it
	// into openPicker's inline popup and reads it back in splitOriginPane.
	allowed := map[string]int{"tmux.go": 2, "ai.go": 2}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	seen := map[string]int{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if count := strings.Count(string(body), key); count != 0 {
			seen[name] = count
		}
	}
	if !reflect.DeepEqual(seen, allowed) {
		t.Fatalf("TMUX_SPLIT_TARGET_PANE occurrences = %v, want %v; the origin pane belongs to the split UI", seen, allowed)
	}
	// The read has exactly one spelling, in splitOriginPane, so no second
	// consumer can grow its own idea of what the origin pane scopes.
	body, err := os.ReadFile("ai.go")
	if err != nil {
		t.Fatalf("read ai.go: %v", err)
	}
	if got := strings.Count(string(body), `c.env(`+key+`)`); got != 1 {
		t.Fatalf("ai.go reads the origin pane %d times, want exactly one read in splitOriginPane", got)
	}
	for _, name := range []string{"active_target.go", "active_project_scope.go", "create_resource.go"} {
		body, err = os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(string(body), key) {
			t.Fatalf("%s reads the split origin pane; the active-target seam takes an anchor from its caller", name)
		}
	}
}

// popupOriginLookupEnv is testAICommand's environment plus the popup's origin
// pane.
func popupOriginLookupEnv(home, pane string) func(string) string {
	return func(key string) string {
		switch key {
		case "HOME":
			return home
		case "TMUX":
			return "/tmp/tmux-1000/projmux,7,0"
		case "TMUX_SPLIT_TARGET_PANE":
			return pane
		default:
			return ""
		}
	}
}
