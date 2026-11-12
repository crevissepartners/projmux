package app

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/notify"
)

// unmarkedShellOptions describes what the inherited server answers for an
// app-owned shell Pane that carries no provider marker.
type unmarkedShellOptions struct {
	appMarker string
	paneUID   string
	agent     string
}

const unmarkedShellSocket = "/tmp/app-shell/socket"

// unmarkedShellCommand builds a hook command whose environment is the one a
// provider started from an app shell Pane inherits, with a queue store that the
// test asserts directly. Any Registry write fails the test.
func unmarkedShellCommand(t *testing.T, tmuxPane string, opts unmarkedShellOptions) (*aiCommand, *stubNotifyStore) {
	t.Helper()
	cmd := testAICommand(t.TempDir())
	base := cmd.lookupEnv
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "TMUX":
			return unmarkedShellSocket + ",123,0"
		case "TMUX_PANE":
			return tmuxPane
		case desktopNotifyModeEnv:
			return "off"
		}
		return base(name)
	}
	cmd.updateRegistry = func(func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
		t.Error("an unmarked shell hook wrote the Registry")
		return coremetadata.Registry{}, nil
	}
	store := &stubNotifyStore{}
	cmd.producer = &storeAttentionNotifyProducer{store: store}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		if len(args) < 2 || !reflect.DeepEqual(args[:2], []string{"-S", unmarkedShellSocket}) {
			t.Fatalf("shell hook escaped the inherited route: %q", args)
		}
		plain := args[2:]
		if len(plain) > 0 && plain[0] == "display-message" {
			values := map[string]string{
				"#{" + aiPaneAgentOption + "}": opts.agent,
				"#{@projmux_app}":              opts.appMarker,
				"#{@projmux_pane_uid}":         opts.paneUID,
				"#S":                           "workspace", "#{window_id}": "@1",
				"#{pane_id}": tmuxPane, "#{socket_path}": unmarkedShellSocket,
			}
			return []byte(values[plain[len(plain)-1]]), nil
		}
		return nil, nil
	}
	return cmd, store
}

func unmarkedShellHookRun(t *testing.T, cmd *aiCommand, provider, payload string, extraArgs ...string) {
	t.Helper()
	cmd.stdin = strings.NewReader(payload)
	args := append([]string{"ingest", provider + "-hook"}, extraArgs...)
	if err := cmd.Run(args, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}

var unmarkedShellPayloads = []struct {
	name, provider, payload string
}{
	{"claude-stop", "claude", `{"hook_event_name":"Stop","session_id":"shell-session","last_assistant_message":"done"}`},
	{"claude-notification", "claude", `{"hook_event_name":"Notification","notification_type":"permission_prompt","session_id":"shell-session","message":"Claude needs your permission to use Bash"}`},
	{"codex-stop", "codex", `{"hook_event_name":"Stop","session_id":"shell-session","turn_id":"t1","last_assistant_message":"done"}`},
}

func assertNoShellPromotion(t *testing.T, cmd *aiCommand) {
	t.Helper()
	for _, option := range []string{aiPaneManagedOption, aiPaneAgentOption} {
		if hasRecordedAISetOption(cmdRecorder(cmd).commands, option) {
			t.Errorf("shell Pane was marked through %s", option)
		}
	}
}

// An exact inherited Pane identity that the app owns is queued even though the
// Pane carries no provider marker; the queue entry is read straight from the
// store, and notification attribution authors no Agent and no marker.
func TestHookQueuesNotificationFromUnmarkedAppOwnedShellPane(t *testing.T) {
	for _, tc := range unmarkedShellPayloads {
		t.Run(tc.name, func(t *testing.T) {
			cmd, store := unmarkedShellCommand(t, "%7", unmarkedShellOptions{appMarker: "1", paneUID: "pane-shell"})
			unmarkedShellHookRun(t, cmd, tc.provider, tc.payload)
			if len(store.pushed) != 1 {
				t.Fatalf("queue pushes = %d, want 1", len(store.pushed))
			}
			got := store.pushed[0]
			if got.Target.Pane != "%7" || got.Target.Socket != unmarkedShellSocket || got.Target.Session != "workspace" {
				t.Errorf("wrong queue target: %+v", got.Target)
			}
			if got.Metadata[notify.MetaAgent] != tc.provider {
				t.Errorf("queue entry is not attributed to %s: %+v", tc.provider, got.Metadata)
			}
			assertNoShellPromotion(t, cmd)
		})
	}
}

// A standalone (foreign) server, a Pane the app never gave a UID, and a
// provider-less manual toggle all stay out of the queue.
func TestHookUnmarkedShellPaneQueuesNothingWithoutAppOwnedExactPane(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts unmarkedShellOptions
		pane string
	}{
		{"foreign-standalone-socket", unmarkedShellOptions{appMarker: "", paneUID: "pane-shell"}, "%7"},
		{"app-pane-without-uid", unmarkedShellOptions{appMarker: "1"}, "%7"},
		{"no-identity", unmarkedShellOptions{appMarker: "1", paneUID: "pane-shell"}, ""},
	} {
		for _, p := range unmarkedShellPayloads {
			t.Run(tc.name+"/"+p.name, func(t *testing.T) {
				cmd, store := unmarkedShellCommand(t, tc.pane, tc.opts)
				unmarkedShellHookRun(t, cmd, p.provider, p.payload)
				if len(store.pushed) != 0 {
					t.Fatalf("queued %d entries: %+v", len(store.pushed), store.pushed)
				}
				assertNoShellPromotion(t, cmd)
			})
		}
	}
}

// A working-directory match between two Panes is a guess, not an identity.
func TestHookUnmarkedShellPaneAmbiguousCWDQueuesNothing(t *testing.T) {
	for _, p := range unmarkedShellPayloads {
		t.Run(p.name, func(t *testing.T) {
			cmd, store := unmarkedShellCommand(t, "", unmarkedShellOptions{appMarker: "1", paneUID: "pane-shell"})
			read := cmd.readCommand
			cmd.readCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if len(args) > 2 && args[2] == "list-panes" {
					return []byte("%7\x1f/same/cwd\x1f\x1f\n%8\x1f/same/cwd\x1f\x1f\n"), nil
				}
				return read(ctx, name, args...)
			}
			payload := strings.Replace(p.payload, `"session_id"`, `"cwd":"/same/cwd","session_id"`, 1)
			unmarkedShellHookRun(t, cmd, p.provider, payload)
			if len(store.pushed) != 0 {
				t.Fatalf("ambiguous cwd queued %d entries: %+v", len(store.pushed), store.pushed)
			}
		})
	}
}

// An explicit identity the Registry does not know, and a detached hook for a
// conversation no Pane claims, are never guessed onto a Pane.
func TestHookUnmarkedShellPaneUnknownIdentityQueuesNothing(t *testing.T) {
	for _, p := range unmarkedShellPayloads {
		t.Run("explicit-unknown/"+p.name, func(t *testing.T) {
			cmd, store := unmarkedShellCommand(t, "%7", unmarkedShellOptions{appMarker: "1", paneUID: "pane-shell"})
			cmd.loadRegistry = func() (coremetadata.Registry, error) { return coremetadata.Registry{}, nil }
			unmarkedShellHookRun(t, cmd, p.provider, p.payload, "--pane=pane-unknown")
			if len(store.pushed) != 0 {
				t.Fatalf("unknown explicit pane queued %d entries", len(store.pushed))
			}
		})
		t.Run("detached-unregistered/"+p.name, func(t *testing.T) {
			cmd, store := unmarkedShellCommand(t, "%7", unmarkedShellOptions{appMarker: "1", paneUID: "pane-shell"})
			base := cmd.lookupEnv
			cmd.lookupEnv = func(name string) string {
				if name == "TMUX" || name == "TMUX_PANE" {
					return ""
				}
				return base(name)
			}
			cmd.loadRegistry = func() (coremetadata.Registry, error) { return coremetadata.Registry{}, nil }
			unmarkedShellHookRun(t, cmd, p.provider, p.payload)
			if len(store.pushed) != 0 {
				t.Fatalf("unregistered conversation queued %d entries", len(store.pushed))
			}
		})
	}
}

// The manual attention toggle carries no hook provider, so an app-owned Pane
// without a marker stays out of the queue exactly as before.
func TestManualAttentionPushOnUnmarkedAppOwnedShellPaneQueuesNothing(t *testing.T) {
	store := &stubNotifyStore{}
	producer := &storeAttentionNotifyProducer{store: store}
	producer.PushReplyReady(attentionNotifyInput{PaneID: "%7", Lookup: unmarkedAppPaneLookup{}})
	if len(store.pushed) != 0 {
		t.Fatalf("manual toggle queued %d entries", len(store.pushed))
	}
}

// A marked Pane keeps queueing under its own marker and is not re-attributed to
// the hook's provider.
func TestHookMarkedPaneStillQueuesUnderItsMarker(t *testing.T) {
	cmd, store := unmarkedShellCommand(t, "%7", unmarkedShellOptions{appMarker: "", paneUID: "", agent: "claude"})
	unmarkedShellHookRun(t, cmd, "claude", unmarkedShellPayloads[0].payload)
	if len(store.pushed) != 1 {
		t.Fatalf("marked pane queue pushes = %d, want 1", len(store.pushed))
	}
}

// Shared app-server regression: a detached Codex hook (no TMUX, no --pane) for
// a conversation the Registry records on a managed Pane still queues from the
// Pane's own marker, on the app route, with the Registry-attributed target.
func TestHookSharedAppServerRegisteredConversationStillQueues(t *testing.T) {
	registry := conversationRegistry("pane-owned", "%7", &coremetadata.CodexActivationBinding{ThreadID: "owned"}, nil)
	registry.Agents[0].Spec.Provider = "codex"
	cmd := testAICommand(t.TempDir())
	cmd.loadRegistry = func() (coremetadata.Registry, error) { return registry.Clone(), nil }
	cmd.updateRegistry = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
		working := registry.Clone()
		if err := fn(&working); err != nil {
			return coremetadata.Registry{}, err
		}
		registry = working.Normalize()
		return registry.Clone(), nil
	}
	store := &stubNotifyStore{}
	cmd.producer = &storeAttentionNotifyProducer{store: store}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		if _, ok := testAIPaneRouteProbe(name, args); ok {
			return codexHookDeliveryRouteRow("%7", "pane-owned"), nil
		}
		plain := stripRecordedTmuxRoute(args)
		if len(plain) > 0 && plain[0] == "display-message" {
			values := map[string]string{
				"#{" + aiPaneAgentOption + "}": "codex",
				"#S":                           "workspace", "#{window_id}": "@1",
				"#{pane_id}": "%7", "#{socket_path}": "/tmp/app/socket",
			}
			return []byte(values[plain[len(plain)-1]]), nil
		}
		return nil, nil
	}
	cmd.stdin = strings.NewReader(`{"hook_event_name":"Stop","thread_id":"owned","turn_id":"t1","cwd":"/srv/alpha","last_assistant_message":"done"}`)
	if err := cmd.Run([]string{"ingest", "codex-hook"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(store.pushed) != 1 || store.pushed[0].Target.Pane != "%7" || store.pushed[0].Target.Socket != "/tmp/app/socket" {
		t.Fatalf("shared app-server hook queue = %+v, want one entry for %%7", store.pushed)
	}
}

// unmarkedAppPaneLookup answers like a server that owns an unmarked shell Pane.
type unmarkedAppPaneLookup struct{}

func (unmarkedAppPaneLookup) PaneOption(string, string) string { return "" }

func (unmarkedAppPaneLookup) PaneFormat(_, format string) string {
	switch format {
	case "#{@projmux_app}":
		return "1"
	case "#{@projmux_pane_uid}":
		return "pane-shell"
	case "#S":
		return "workspace"
	}
	return ""
}
