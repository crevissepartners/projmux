package app

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// A detached hook must read its producer target and desktop state from the
// same server as its reflection writes. The unrelated default server answers
// nothing; recording every attempted call also catches best-effort pollution.
func TestManagedHookNotificationWithoutTmuxUsesAppRoute(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		for _, event := range []string{"Stop", "PermissionRequest"} {
			t.Run(provider+"/"+event, func(t *testing.T) {
				cmd := testAICommand(t.TempDir())
				registry := resourceFixtureRegistry(t)
				pane, _ := registry.Pane("pan-alpha-codex")
				pane.Status.Activation.RuntimeID = "%7"
				pane.Status.Activation.AgentUID = "agt-alpha-codex"
				paneID := pane.Status.Activation.RuntimeID
				agent, _ := registry.Agent(pane.Status.Activation.AgentUID)
				agent.Spec.Provider = provider
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
				var bare [][]string
				cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
					if name != "tmux" {
						return nil, os.ErrNotExist
					}
					plain := stripRecordedTmuxRoute(args)
					if len(plain) == len(args) {
						bare = append(bare, append([]string(nil), args...))
						return nil, os.ErrNotExist
					}
					if !reflect.DeepEqual(args[:2], []string{"-L", defaultAppSocket}) {
						t.Errorf("wrong route: %q", args)
					}
					if _, ok := testAIPaneRouteProbe(name, args); ok {
						return codexHookDeliveryRouteRow(paneID, "pan-alpha-codex"), nil
					}
					if len(plain) > 0 && plain[0] == "display-message" {
						format := plain[len(plain)-1]
						values := map[string]string{
							"#{" + aiPaneAgentOption + "}": provider,
							"#{@projmux_pane_uid}":         "pan-alpha-codex",
							"#S":                           "workspace", "#{window_id}": "@1",
							"#{pane_id}": paneID, "#{socket_path}": "/tmp/app/socket",
						}
						return []byte(values[format]), nil
					}
					return nil, nil
				}
				cmd.runCommand = func(_ context.Context, name string, args ...string) error {
					if name == "tmux" && len(stripRecordedTmuxRoute(args)) == len(args) {
						bare = append(bare, append([]string(nil), args...))
					}
					return nil
				}
				payload := `{"hook_event_name":"` + event + `","session_id":"route-session","cwd":"/srv/alpha","last_assistant_message":"done","tool_name":"Bash","tool_input":{"command":"ls"}}`
				if provider == "claude" && event == "PermissionRequest" {
					payload = `{"hook_event_name":"Notification","notification_type":"permission_prompt","session_id":"route-session","cwd":"/srv/alpha","message":"Claude needs your permission to use Bash"}`
				}
				cmd.stdin = strings.NewReader(payload)
				if err := cmd.Run([]string{"ingest", provider + "-hook", "--pane=pan-alpha-codex"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
					t.Fatal(err)
				}
				if len(store.pushed) != 1 {
					t.Errorf("queue pushes = %d, want 1", len(store.pushed))
				} else if target := store.pushed[0].Target; target.Pane != paneID || target.Socket != "/tmp/app/socket" || target.Session != "workspace" {
					t.Errorf("wrong queue target: %+v", target)
				}
				if len(bare) != 0 {
					t.Errorf("hook attempted %d bare tmux calls: %q", len(bare), bare)
				}
			})
		}
	}
}

// A reachable unrelated server must not turn an otherwise unregistered hook
// into a cwd match. Detached attribution reads Registry identity only, never
// tmux's ambient inventory (not even when it would return a plausible row).
func TestManagedHookDetachedAttributionNeverProbesAmbientInventory(t *testing.T) {
	for _, tc := range []struct {
		name, conversation, pane, reason string
		mutate                           func(*coremetadata.Registry)
	}{
		{name: "registered", conversation: "owned", pane: "%7"},
		{name: "same-cwd-new-session", conversation: "unregistered", reason: aiPaneMatchReasonConversationUnknown},
		{name: "ambiguous", conversation: "owned", reason: aiPaneMatchReasonConversationShared, mutate: func(r *coremetadata.Registry) {
			second := conversationRegistry("pane-second", "%8", &coremetadata.CodexActivationBinding{ThreadID: "owned"}, nil)
			r.Agents = append(r.Agents, second.Agents...)
			r.Panes = append(r.Panes, second.Panes...)
		}},
		{name: "stale", conversation: "owned", reason: aiPaneMatchReasonConversationUnknown, mutate: func(r *coremetadata.Registry) {
			r.Agents[0].Status.PaneRef = "pane-no-longer-owned"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := conversationRegistry("pane-owned", "%7", &coremetadata.CodexActivationBinding{ThreadID: "owned"}, nil)
			if tc.mutate != nil {
				tc.mutate(&registry)
			}
			cmd := sharedHostCommand(t, registry)
			var probes int
			cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name == "tmux" {
					probes++
					return []byte("%99\x1f/same/cwd\x1fowned\x1fowned\n"), nil
				}
				return nil, os.ErrNotExist
			}
			pane, reason := cmd.matchAIPane(aiPaneMatchInput{Provider: "codex", CWD: "/same/cwd", ThreadID: tc.conversation})
			if pane != tc.pane || reason != tc.reason {
				t.Errorf("attribution = (%q, %q), want (%q, %q)", pane, reason, tc.pane, tc.reason)
			}
			if probes != 0 {
				t.Errorf("detached attribution attempted %d tmux probes", probes)
			}
		})
	}
}

func TestManagedHookBoundaryPreservesExactTransportAndNonHookSeams(t *testing.T) {
	for _, inherited := range []bool{false, true} {
		t.Run(map[bool]string{false: "app", true: "inherited"}[inherited], func(t *testing.T) {
			cmd := testAICommand(t.TempDir())
			prefix := []string{"-L", defaultAppSocket}
			if inherited {
				cmd.lookupEnv = func(name string) string {
					if name == "TMUX" {
						return "/tmp/custom/socket,123,0"
					}
					return ""
				}
				prefix = []string{"-S", "/tmp/custom/socket"}
			}
			var reads, writes [][]string
			available := true
			cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
				if row, ok := testAIPaneRouteProbe(name, args); ok {
					if !available {
						return nil, os.ErrNotExist
					}
					return row, nil
				}
				reads = append(reads, append([]string(nil), args...))
				return []byte("value"), nil
			}
			cmd.runCommand = func(_ context.Context, _ string, args ...string) error {
				writes = append(writes, append([]string(nil), args...))
				return nil
			}
			hook := cmd.withAIHookPaneRoute(aiHookPaneIdentity{paneID: "%7", paneUID: "test-pane-uid"})
			if got := hook.notifyLookup().PaneOption("%7", aiPaneAgentOption); got != "value" {
				t.Fatalf("lookup = %q", got)
			}
			hook.paneVisibleToClient("%7")
			hook.recordAINotification("%7", "key")
			for _, args := range append(reads, writes...) {
				if len(args) < 2 || !reflect.DeepEqual(args[:2], prefix) {
					t.Errorf("hook escaped its transport: %q, want %q", args, prefix)
				}
			}
			// No wrapper is installed on the reusable/non-hook command.
			cmd.readTmuxPaneOption("%7", aiPaneAgentOption)
			if got := reads[len(reads)-1]; len(stripRecordedTmuxRoute(got)) != len(got) {
				t.Errorf("non-hook seam changed: %q", got)
			}
			if !inherited {
				available = false
				beforeReads, beforeWrites := len(reads), len(writes)
				if got := hook.notifyLookup().PaneOption("%7", aiPaneAgentOption); got != "" {
					t.Errorf("refused route still returned %q", got)
				}
				hook.recordAINotification("%7", "another")
				if len(reads) != beforeReads || len(writes) != beforeWrites {
					t.Error("disappeared app route fell back or retained a cached receipt")
				}
			}
		})
	}
}

// Detached app containment must name the attributed resource, not just some
// resource that reused its runtime handle. Inherited exact receipts retain
// their established shell-observation policy (covered separately).
func TestManagedHookBoundaryRefusesReusedRuntimeID(t *testing.T) {
	registry := conversationRegistry("pane-owned", "%7", nil, nil)
	cmd := sharedHostCommand(t, registry)
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" || len(args) < 2 || len(stripRecordedTmuxRoute(args)) == len(args) {
			t.Fatalf("identity probe escaped transport: %s %q", name, args)
		}
		if _, ok := testAIPaneRouteProbe(name, args); ok {
			return codexHookDeliveryRouteRow("%7", "pane-replacement"), nil
		}
		return []byte("pane-replacement"), nil
	}
	hook := cmd.withAIHookPaneRoute(aiHookPaneIdentity{paneID: "%7", paneUID: "pane-owned"})
	store := &stubNotifyStore{}
	producer := &storeAttentionNotifyProducer{store: store}
	producer.PushReplyReady(attentionNotifyInput{PaneID: "%7", Lookup: hook.notifyLookup()})
	if err := hook.setAIPaneOption("%7", aiPaneStateOption, "waiting"); err == nil {
		t.Error("stale runtime still reflected state")
	}
	if len(store.pushed) != 0 || len(cmdRecorder(cmd).commands) != 0 {
		t.Error("stale runtime wrote a queue entry or pane option")
	}
}

func TestManagedHookDetachedConversationIDsMustAgree(t *testing.T) {
	registry := conversationRegistry("pane-thread", "%7", &coremetadata.CodexActivationBinding{ThreadID: "thread-one"}, nil)
	second := conversationRegistry("pane-session", "%8", nil, &coremetadata.AgentSessionRef{Provider: "codex", Codex: &coremetadata.CodexSessionRef{ThreadID: "session-two"}})
	registry.Agents = append(registry.Agents, second.Agents...)
	registry.Panes = append(registry.Panes, second.Panes...)
	cmd := sharedHostCommand(t, registry)
	for _, tc := range []struct{ thread, session, pane, reason string }{
		{"thread-one", "session-two", "", aiPaneMatchReasonConversationShared},
		{"thread-one", "thread-one", "%7", ""},
		{"unregistered", "session-two", "%8", ""},
	} {
		pane, reason := cmd.matchAIPane(aiPaneMatchInput{Provider: "codex", ThreadID: tc.thread, SessionID: tc.session, CWD: "/same/cwd"})
		if pane != tc.pane || reason != tc.reason {
			t.Errorf("thread=%q session=%q: (%q,%q), want (%q,%q)", tc.thread, tc.session, pane, reason, tc.pane, tc.reason)
		}
	}
}

// aiHookTestEnvironment makes a scenario's server receipt explicit. It is
// opt-in at each inherited-inventory fixture; the common testAICommand and all
// detached fixtures keep an empty TMUX. It never changes the process env.
func aiHookTestEnvironment(base func(string) string, receipt string) func(string) string {
	return func(name string) string {
		if name == "TMUX" {
			return receipt
		}
		return base(name)
	}
}

func TestManagedHookInheritedUnmanagedShellStillQueuesNothing(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			cmd := testAICommand(t.TempDir())
			base := cmd.lookupEnv
			cmd.lookupEnv = func(name string) string {
				switch name {
				case "TMUX":
					return "/tmp/unmanaged/socket,123,0"
				case "TMUX_PANE":
					return "%7"
				case desktopNotifyModeEnv:
					return "off"
				}
				return base(name)
			}
			store := &stubNotifyStore{}
			cmd.producer = &storeAttentionNotifyProducer{store: store}
			cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name != "tmux" {
					return nil, os.ErrNotExist
				}
				if len(args) < 2 || !reflect.DeepEqual(args[:2], []string{"-S", "/tmp/unmanaged/socket"}) {
					t.Fatalf("unmanaged hook escaped inherited route: %q", args)
				}
				// No provider/managed/UID marker exists on this shell.
				return nil, nil
			}
			cmd.stdin = strings.NewReader(`{"hook_event_name":"Stop","session_id":"shell-session","last_assistant_message":"done"}`)
			if err := cmd.Run([]string{"ingest", provider + "-hook"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			if len(store.pushed) != 0 {
				t.Fatalf("unmanaged shell queued %d notifications", len(store.pushed))
			}
			commands := cmdRecorder(cmd).commands
			if !hasRecordedAISetOption(commands, aiPaneStateOption) {
				t.Error("inherited transient shell observation was lost")
			}
			for _, option := range []string{aiPaneManagedOption, aiPaneAgentOption} {
				if hasRecordedAISetOption(commands, option) {
					t.Errorf("shell was promoted through %s", option)
				}
			}
			for _, command := range commands {
				if command.name == "tmux" && (len(command.args) < 2 || !reflect.DeepEqual(command.args[:2], []string{"-S", "/tmp/unmanaged/socket"})) {
					t.Errorf("shell write escaped: %q", command.args)
				}
			}
		})
	}
}

func TestManagedHookProcessClaudeWithoutRuntimeQueuesNothing(t *testing.T) {
	registry := conversationRegistry("pane-process", "", nil, nil)
	registry.Agents[0].Spec.Provider = "claude"
	cmd := sharedHostCommand(t, registry)
	store := &stubNotifyStore{}
	cmd.producer = &storeAttentionNotifyProducer{store: store}
	cmd.readCommand = func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("process Claude hook probed tmux without a runtime")
		return nil, os.ErrNotExist
	}
	cmd.runCommand = func(context.Context, string, ...string) error {
		t.Fatal("process Claude hook attempted a runtime effect")
		return nil
	}
	cmd.stdin = strings.NewReader(`{"hook_event_name":"Stop","session_id":"process-session"}`)
	if err := cmd.Run([]string{"ingest", "claude-hook", "--pane=pane-process"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(store.pushed) != 0 {
		t.Errorf("process Claude hook queued %d entries", len(store.pushed))
	}
	path, _ := cmd.aiIngestLogPath()
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), aiPaneMatchReasonExplicitNoRuntime) {
		t.Errorf("missing no-runtime refusal: %s, %v", data, err)
	}
}

// Legacy app-route semantic fixtures already supply their tmux option values;
// give them only the explicit resource identity needed to reach that route.
// This does not add TMUX, a Running Agent, or a real Registry write seam.
func aiHookTestExplicitPane(cmd *aiCommand, paneID string) string {
	const uid = "test-pane-uid"
	registry := hookIdentityRegistry(uid, paneID)
	cmd.loadRegistry = func() (coremetadata.Registry, error) { return registry.Clone(), nil }
	return uid
}

func TestManagedHookDetachedIdentitySurvivesRuntimeReuse(t *testing.T) {
	registry := conversationRegistry("pane-original", "%7", &coremetadata.CodexActivationBinding{ThreadID: "owned"}, nil)
	registry.Agents[0].Spec.Provider = "codex"
	cmd := sharedHostCommand(t, registry)
	loads := 0
	cmd.loadRegistry = func() (coremetadata.Registry, error) {
		loads++
		return registry.Clone(), nil
	}
	identity, reason := cmd.matchAIHookPane(aiPaneMatchInput{Provider: "codex", ThreadID: "owned"})
	if reason != "" || identity.paneID != "%7" || identity.paneUID != "pane-original" || loads != 1 {
		t.Fatalf("attribution = %+v, reason=%q, loads=%d", identity, reason, loads)
	}
	// Replace the Registry occupant after attribution. Its actual tmux
	// containment proof agrees with the new Registry, not with this hook.
	registry = conversationRegistry("pane-replacement", "%7", &coremetadata.CodexActivationBinding{ThreadID: "other"}, nil)
	registry.Agents[0].Spec.Provider = "codex"
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if _, ok := testAIPaneRouteProbe(name, args); !ok {
			t.Fatalf("unexpected proof %s %q", name, args)
		}
		return codexHookDeliveryRouteRow("%7", "pane-replacement"), nil
	}
	hook := cmd.withAIHookPaneRoute(identity)
	store := &stubNotifyStore{}
	producer := &storeAttentionNotifyProducer{store: store}
	producer.PushReplyReady(attentionNotifyInput{PaneID: identity.paneID, Lookup: hook.notifyLookup()})
	if err := hook.setAIPaneOption(identity.paneID, aiPaneStateOption, "waiting"); err == nil {
		t.Error("replacement received reflection")
	}
	if loads != 1 || len(store.pushed) != 0 || len(cmdRecorder(cmd).commands) != 0 {
		t.Errorf("identity was re-attributed or wrote: loads=%d queue=%d writes=%d", loads, len(store.pushed), len(cmdRecorder(cmd).commands))
	}
}
