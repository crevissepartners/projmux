package app

import (
	"bytes"
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// sessionRefObservedAt is the deterministic ingest clock of these tests.
var sessionRefObservedAt = time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)

// sessionRefHarness wires an aiCommand against an in-memory registry and a
// recorded tmux transport, so an ingest run can be judged on both the pane
// options it wrote and the Agent resource it updated.
type sessionRefHarness struct {
	cmd      *aiCommand
	registry *coremetadata.Registry
	agentUID string
	paneUID  string

	tmuxCalls []string
	updates   int
	loads     int
}

func newSessionRefHarness(t *testing.T, provider string) *sessionRefHarness {
	t.Helper()

	mutator := coremetadata.Mutator{
		Now:       func() time.Time { return sessionRefObservedAt },
		NewUID:    sequentialTestUID(),
		DirExists: func(string) (bool, error) { return true, nil },
	}
	registry := &coremetadata.Registry{APIVersion: coremetadata.APIVersion, SchemaVersion: coremetadata.SchemaVersion}
	project, err := mutator.RegisterProject(registry, coremetadata.RegisterProjectOptions{
		Root:         "/src/app",
		DefaultShell: "/bin/zsh",
		OperationID:  "op-1",
	})
	if err != nil {
		t.Fatalf("register project: %v", err)
	}
	agent, err := mutator.CreateAgent(registry, project.Windows[0].Metadata.UID, coremetadata.CreateAgentOptions{
		Provider:    provider,
		OperationID: "op-2",
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	pane, err := mutator.AttachAgentPane(registry, agent.Metadata.UID, coremetadata.BootstrapPane{
		Command: provider,
		CWD:     "/src/app",
	}, "op-3")
	if err != nil {
		t.Fatalf("attach agent pane: %v", err)
	}

	h := &sessionRefHarness{registry: registry, agentUID: agent.Metadata.UID, paneUID: pane.Metadata.UID}
	home := t.TempDir()
	h.cmd = &aiCommand{
		lookupEnv: func(name string) string {
			switch name {
			case "HOME":
				return home
			case "TMUX_PANE":
				// Ingest attributes the event to the inherited pane, which is the
				// production path a provider hook runs through.
				return "%7"
			default:
				return ""
			}
		},
		homeDir:   func() (string, error) { return home, nil },
		readFile:  func(string) ([]byte, error) { return nil, os.ErrNotExist },
		writeFile: os.WriteFile,
		mkdirAll:  os.MkdirAll,
		now:       func() time.Time { return sessionRefObservedAt },
		sleep:     func(time.Duration) {},
		runCommand: func(_ context.Context, name string, args ...string) error {
			h.tmuxCalls = append(h.tmuxCalls, name+" "+strings.Join(args, " "))
			return nil
		},
		readCommand: func(_ context.Context, name string, args ...string) ([]byte, error) {
			h.tmuxCalls = append(h.tmuxCalls, name+" "+strings.Join(args, " "))
			if len(args) >= 5 && args[0] == "display-message" && args[4] == "#{"+tmuxopts.PaneUID+"}" {
				return []byte(h.paneUID + "\n"), nil
			}
			return nil, os.ErrNotExist
		},
		loadRegistry: func() (coremetadata.Registry, error) {
			h.loads++
			return h.registry.Clone(), nil
		},
	}
	h.cmd.updateRegistry = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
		h.updates++
		working := h.registry.Clone()
		if err := fn(&working); err != nil {
			// The real store performs no write at all when the operation fails.
			return coremetadata.Registry{}, err
		}
		if err := working.Validate(); err != nil {
			return coremetadata.Registry{}, err
		}
		*h.registry = working
		return working.Clone(), nil
	}
	return h
}

func (h *sessionRefHarness) agent(t *testing.T) coremetadata.Agent {
	t.Helper()
	agent, ok := h.registry.Agent(h.agentUID)
	if !ok {
		t.Fatalf("agent %s disappeared", h.agentUID)
	}
	return agent.Clone()
}

func (h *sessionRefHarness) ingest(t *testing.T, args []string, payload string) {
	t.Helper()
	h.cmd.stdin = strings.NewReader(payload)
	var stdout, stderr bytes.Buffer
	if err := h.cmd.runIngest(args, &stdout, &stderr); err != nil {
		t.Fatalf("runIngest %v: %v (stderr=%s)", args, err, stderr.String())
	}
}

func sequentialTestUID() func(coremetadata.Kind) (string, error) {
	counts := map[coremetadata.Kind]int{}
	return func(kind coremetadata.Kind) (string, error) {
		counts[kind]++
		return strings.ToLower(string(kind)) + "-0" + string(rune('0'+counts[kind])), nil
	}
}

// TestOneProviderHookRecordsItsOwnConversationShapeOnTheAgent is the
// per-provider ingest table. Each provider goes through its real `ai ingest`
// entry point with a real hook payload, and each one is judged on the union
// member it populated -- Claude's session id plus transcript path, Codex's
// thread and session ids, Antigravity's single conversation id.
func TestOneProviderHookRecordsItsOwnConversationShapeOnTheAgent(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		args     []string
		payload  string
		want     *coremetadata.AgentSessionRef
	}{
		{
			name:     "claude reports a session id and a transcript path",
			provider: "claude",
			args:     []string{"claude-hook"},
			payload: `{"hook_event_name":"UserPromptSubmit","session_id":"claude-session-1",` +
				`"transcript_path":"/home/u/.claude/projects/app/claude-session-1.jsonl","cwd":"/src/app"}`,
			want: &coremetadata.AgentSessionRef{
				Provider:   "claude",
				ObservedAt: sessionRefObservedAt,
				Claude: &coremetadata.ClaudeSessionRef{
					SessionID:      "claude-session-1",
					TranscriptPath: "/home/u/.claude/projects/app/claude-session-1.jsonl",
				},
			},
		},
		{
			name:     "codex reports a thread id beside its session id",
			provider: "codex",
			args:     []string{"codex-hook"},
			payload: `{"hook_event_name":"UserPromptSubmit","thread_id":"codex-thread-1",` +
				`"session_id":"codex-session-1","turn_id":"codex-turn-1","cwd":"/src/app"}`,
			want: &coremetadata.AgentSessionRef{
				Provider:   "codex",
				ObservedAt: sessionRefObservedAt,
				Codex: &coremetadata.CodexSessionRef{
					ThreadID:  "codex-thread-1",
					SessionID: "codex-session-1",
				},
			},
		},
		{
			name:     "antigravity reports one conversation id",
			provider: "antigravity",
			args:     []string{"antigravity-hook", "--event", "PreInvocation"},
			payload:  `{"conversationId":"antigravity-conversation-1","workspacePaths":["/src/app"]}`,
			want: &coremetadata.AgentSessionRef{
				Provider:    "antigravity",
				ObservedAt:  sessionRefObservedAt,
				Antigravity: &coremetadata.AntigravitySessionRef{ConversationID: "antigravity-conversation-1"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newSessionRefHarness(t, tc.provider)
			if before := h.agent(t); before.Status.SessionRef != nil {
				t.Fatal("the fixture Agent already carries a session ref")
			}

			h.ingest(t, tc.args, tc.payload)

			got := h.agent(t).Status.SessionRef
			if got == nil {
				t.Fatalf("no session ref recorded; tmux calls = %v", h.tmuxCalls)
			}
			if got.Provider != tc.want.Provider || !got.ObservedAt.Equal(tc.want.ObservedAt) {
				t.Fatalf("ref header = %s/%s, want %s/%s", got.Provider, got.ObservedAt, tc.want.Provider, tc.want.ObservedAt)
			}
			if !got.SameConversation(tc.want) {
				t.Fatalf("ref = %#v, want %#v", got, tc.want)
			}
			// A per-provider union means the other members stay nil.
			switch tc.provider {
			case "claude":
				if got.Codex != nil || got.Antigravity != nil {
					t.Fatalf("claude ref populated another provider member: %#v", got)
				}
			case "codex":
				if got.Claude != nil || got.Antigravity != nil {
					t.Fatalf("codex ref populated another provider member: %#v", got)
				}
			case "antigravity":
				if got.Claude != nil || got.Codex != nil {
					t.Fatalf("antigravity ref populated another provider member: %#v", got)
				}
			}
			if err := h.registry.Validate(); err != nil {
				t.Fatalf("registry invalid after ingest: %v", err)
			}
		})
	}
}

// TestIngestStillWritesThePaneSessionOptionUnchanged is acceptance criterion 5:
// the Agent field is a second, additive home and the live routing index the
// ingest matcher depends on is written exactly as before.
func TestIngestStillWritesThePaneSessionOptionUnchanged(t *testing.T) {
	h := newSessionRefHarness(t, "codex")
	h.ingest(t, []string{"codex-hook"},
		`{"hook_event_name":"UserPromptSubmit","thread_id":"codex-thread-1","session_id":"codex-session-1","cwd":"/src/app"}`)

	wantOptions := []string{
		"tmux set-option -p -t %7 " + aiPaneSessionIDOption + " codex-session-1",
		"tmux set-option -p -t %7 " + aiPaneThreadIDOption + " codex-thread-1",
		"tmux set-option -p -t %7 " + aiPaneAgentOption + " codex",
		"tmux set-option -p -t %7 " + aiPaneResumeIDOption + " codex-session-1",
	}
	for _, want := range wantOptions {
		if !slices.Contains(h.tmuxCalls, want) {
			t.Fatalf("missing pane option write %q in %v", want, h.tmuxCalls)
		}
	}
	// Nothing removes or unsets the routing index.
	for _, call := range h.tmuxCalls {
		if strings.Contains(call, "set-option") && strings.Contains(call, "-u") && strings.Contains(call, aiPaneSessionIDOption) {
			t.Fatalf("ingest unset the live routing index: %q", call)
		}
	}
}

// TestAnUnwiredRegistrySeamLeavesIngestExactlyAsItWas keeps the many aiCommand
// fixtures -- and any caller that never wires the seam -- from reaching the real
// state directory just because a hook was ingested.
func TestAnUnwiredRegistrySeamLeavesIngestExactlyAsItWas(t *testing.T) {
	h := newSessionRefHarness(t, "codex")
	h.cmd.loadRegistry = nil
	h.cmd.updateRegistry = nil

	h.ingest(t, []string{"codex-hook"},
		`{"hook_event_name":"UserPromptSubmit","thread_id":"codex-thread-1","cwd":"/src/app"}`)

	if h.updates != 0 || h.loads != 0 {
		t.Fatalf("registry touched with an unwired seam: loads=%d updates=%d", h.loads, h.updates)
	}
	if agent := h.agent(t); agent.Status.SessionRef != nil {
		t.Fatalf("session ref recorded through an unwired seam: %#v", agent.Status.SessionRef)
	}
	for _, call := range h.tmuxCalls {
		if strings.Contains(call, tmuxopts.PaneUID) {
			t.Fatalf("an unwired seam still queried the pane uid: %q", call)
		}
	}
}

// TestIngestNegativesOpenNoRegistryTransaction covers every reason the write is
// skipped. Each one must cost zero write transactions, because a hook that
// cannot be attributed to an Agent has nothing to record.
func TestIngestNegativesOpenNoRegistryTransaction(t *testing.T) {
	tests := []struct {
		name    string
		arrange func(t *testing.T, h *sessionRefHarness)
		payload string
	}{
		{
			name: "the registry holds no Agent at all",
			arrange: func(_ *testing.T, h *sessionRefHarness) {
				h.registry.Agents = nil
			},
			payload: `{"hook_event_name":"UserPromptSubmit","thread_id":"codex-thread-1","cwd":"/src/app"}`,
		},
		{
			name: "the pane mirrors no Projmux uid",
			arrange: func(_ *testing.T, h *sessionRefHarness) {
				h.paneUID = ""
			},
			payload: `{"hook_event_name":"UserPromptSubmit","thread_id":"codex-thread-1","cwd":"/src/app"}`,
		},
		{
			name: "the pane is a Window-owned shell pane, not a managed Agent pane",
			arrange: func(t *testing.T, h *sessionRefHarness) {
				pane, ok := h.registry.Pane(h.paneUID)
				if !ok {
					t.Fatalf("pane %s missing", h.paneUID)
				}
				h.paneUID = h.registry.Windows[0].Spec.PrimaryPaneRef
				_ = pane
			},
			payload: `{"hook_event_name":"UserPromptSubmit","thread_id":"codex-thread-1","cwd":"/src/app"}`,
		},
		{
			name:    "the hook carries no conversation id yet",
			arrange: func(*testing.T, *sessionRefHarness) {},
			payload: `{"hook_event_name":"UserPromptSubmit","cwd":"/src/app"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newSessionRefHarness(t, "codex")
			tc.arrange(t, h)
			h.ingest(t, []string{"codex-hook"}, tc.payload)

			if h.updates != 0 {
				t.Fatalf("opened %d registry write transactions, want 0", h.updates)
			}
			if agent, ok := h.registry.Agent(h.agentUID); ok && agent.Status.SessionRef != nil {
				t.Fatalf("recorded %#v, want nothing", agent.Status.SessionRef)
			}
		})
	}
}

// TestARepeatedHookDoesNotRewriteTheRegistry keeps a hook that fires on every
// turn from opening a write transaction each time.
func TestARepeatedHookDoesNotRewriteTheRegistry(t *testing.T) {
	h := newSessionRefHarness(t, "claude")
	payload := `{"hook_event_name":"UserPromptSubmit","session_id":"claude-session-1","cwd":"/src/app"}`

	h.ingest(t, []string{"claude-hook"}, payload)
	if h.updates != 1 {
		t.Fatalf("first ingest opened %d transactions, want 1", h.updates)
	}
	h.ingest(t, []string{"claude-hook"}, payload)
	h.ingest(t, []string{"claude-hook"}, payload)
	if h.updates != 1 {
		t.Fatalf("re-observing the same conversation opened %d transactions, want 1", h.updates)
	}

	// A different conversation is a real change and is recorded.
	h.ingest(t, []string{"claude-hook"},
		`{"hook_event_name":"UserPromptSubmit","session_id":"claude-session-2","cwd":"/src/app"}`)
	if h.updates != 2 {
		t.Fatalf("a new conversation opened %d transactions, want 2", h.updates)
	}
	if got := h.agent(t).Status.SessionRef.Summary(); got != "claude:claude-session-2" {
		t.Fatalf("session ref = %q, want the newest conversation", got)
	}
}

// TestACrossProviderHookNeverStampsAnAgent proves the provider guard survives
// the whole ingest path: a Claude hook attributed to a Codex Agent's pane
// records nothing and leaves the registry byte-identical.
func TestACrossProviderHookNeverStampsAnAgent(t *testing.T) {
	h := newSessionRefHarness(t, "codex")
	before := h.agent(t)

	h.ingest(t, []string{"claude-hook"},
		`{"hook_event_name":"UserPromptSubmit","session_id":"claude-session-1","cwd":"/src/app"}`)

	after := h.agent(t)
	if after.Status.SessionRef != nil {
		t.Fatalf("a claude hook stamped a codex Agent: %#v", after.Status.SessionRef)
	}
	if after.Status.Phase != before.Status.Phase || after.Status.PaneRef != before.Status.PaneRef {
		t.Fatalf("the refused write disturbed the Agent: %+v", after.Status)
	}
	if err := h.registry.Validate(); err != nil {
		t.Fatalf("registry invalid: %v", err)
	}
}

// TestARecordedSessionRefSurvivesTheAgentGoingOffline is acceptance criterion 2
// through the production release path: the managed Pane is deleted, the Agent
// falls back to Offline with a cleared paneRef, and the conversation pointer is
// still there.
func TestARecordedSessionRefSurvivesTheAgentGoingOffline(t *testing.T) {
	h := newSessionRefHarness(t, "claude")
	h.ingest(t, []string{"claude-hook"},
		`{"hook_event_name":"UserPromptSubmit","session_id":"claude-session-1","cwd":"/src/app"}`)

	mutator := intmetadata.DefaultMutator()
	if _, err := mutator.ReleaseAgentPane(h.registry, h.agentUID, coremetadata.AgentExitNormal, "exited"); err != nil {
		t.Fatalf("release agent pane: %v", err)
	}

	agent := h.agent(t)
	if agent.Status.Phase != coremetadata.PhaseOffline || agent.Status.PaneRef != "" {
		t.Fatalf("agent = %s/%q, want Offline with no paneRef", agent.Status.Phase, agent.Status.PaneRef)
	}
	if got := agent.Status.SessionRef.Summary(); got != "claude:claude-session-1" {
		t.Fatalf("session ref after the Pane died = %q, want it preserved", got)
	}
	if err := h.registry.Validate(); err != nil {
		t.Fatalf("registry invalid: %v", err)
	}
}

// setFixtureSessionRef stamps one conversation onto the fixture's Agent so the
// read routes have something to surface.
func setFixtureSessionRef(t *testing.T, store *fakeResourceStore, uid string, ref *coremetadata.AgentSessionRef) {
	t.Helper()
	agent, ok := store.registry.Agent(uid)
	if !ok {
		t.Fatalf("fixture agent %q missing", uid)
	}
	agent.Status.SessionRef = ref
	if err := store.registry.Validate(); err != nil {
		t.Fatalf("fixture registry invalid: %v", err)
	}
}

// TestDescribeAgentRendersThePerProviderSessionRef is acceptance criterion 1 at
// the read surface. The row keys follow the populated provider member, which is
// the observable difference a flattened single string could not produce.
func TestDescribeAgentRendersThePerProviderSessionRef(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		ref  *coremetadata.AgentSessionRef
		want map[string]string
		gone []string
	}{
		{
			name: "codex renders a thread id and a session id",
			ref: &coremetadata.AgentSessionRef{
				Provider:   "codex",
				ObservedAt: sessionRefObservedAt,
				Codex:      &coremetadata.CodexSessionRef{ThreadID: "codex-thread-1", SessionID: "codex-session-1"},
			},
			want: map[string]string{
				"SessionProvider":   "codex",
				"ThreadID":          "codex-thread-1",
				"SessionID":         "codex-session-1",
				"SessionObservedAt": "2026-08-15T12:00:00Z",
			},
			gone: []string{"TranscriptPath", "ConversationID"},
		},
		{
			name: "an Agent with no observed conversation renders no session rows",
			ref:  nil,
			gone: []string{"SessionProvider", "SessionID", "ThreadID", "ConversationID", "SessionObservedAt"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			setFixtureSessionRef(t, store, "agt-alpha-codex", test.ref)

			stdout, stderr, err := runRoute(t, newTestDescribeCommand(t, store), "agent", "codex", "--project", "alpha")
			if err != nil {
				t.Fatalf("describe agent: %v (stderr=%s)", err, stderr)
			}
			rows := describeRows(t, stdout)
			// The pre-existing rows are untouched.
			for key, want := range map[string]string{"Provider": "codex", "Phase": "Running", "PaneRef": "pan-alpha-codex"} {
				if got := rows[key]; len(got) != 1 || got[0] != want {
					t.Fatalf("row %q = %v, want [%q]\n%s", key, got, want, stdout)
				}
			}
			for key, want := range test.want {
				if got := rows[key]; len(got) != 1 || got[0] != want {
					t.Fatalf("row %q = %v, want [%q]\n%s", key, got, want, stdout)
				}
			}
			for _, key := range test.gone {
				if got := rows[key]; len(got) != 0 {
					t.Fatalf("row %q = %v, want none\n%s", key, got, stdout)
				}
			}
		})
	}
}

// TestGetAgentsSurfacesTheConversationPointer proves the plural read exposes the
// value too, without disturbing the other kinds or the structured modes.
func TestGetAgentsSurfacesTheConversationPointer(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	setFixtureSessionRef(t, store, "agt-alpha-codex", &coremetadata.AgentSessionRef{
		Provider:   "codex",
		ObservedAt: sessionRefObservedAt,
		Codex:      &coremetadata.CodexSessionRef{ThreadID: "codex-thread-1", SessionID: "codex-session-1"},
	})

	stdout, stderr, err := runRoute(t, newTestListGetCommand(t, store), "agents", "--project", "alpha")
	if err != nil {
		t.Fatalf("get agents: %v (stderr=%s)", err, stderr)
	}
	const want = "agent/codex status=live owner=project/alpha window/main session=codex:codex-thread-1\n"
	if stdout != want {
		t.Fatalf("get agents = %q, want %q", stdout, want)
	}

	// An Agent with no observed conversation keeps the pre-change line exactly.
	beta, _, err := runRoute(t, newTestListGetCommand(t, store), "agents", "--project", "beta")
	if err != nil {
		t.Fatalf("get agents --project beta: %v", err)
	}
	if strings.Contains(beta, "session=") {
		t.Fatalf("an Agent with no session ref grew a session field: %q", beta)
	}

	// Other kinds are untouched.
	windows, _, err := runRoute(t, newTestListGetCommand(t, store), "windows", "--project", "beta")
	if err != nil {
		t.Fatalf("get windows: %v", err)
	}
	if windows != "window/main status=offline owner=project/beta\n" {
		t.Fatalf("get windows = %q, want the unchanged summary", windows)
	}

	// The structured mode carries the whole per-provider union.
	structured, _, err := runRoute(t, newTestListGetCommand(t, store), "agents", "--project", "alpha", "-o", "json")
	if err != nil {
		t.Fatalf("get agents -o json: %v", err)
	}
	for _, needle := range []string{`"sessionRef"`, `"provider": "codex"`, `"threadId": "codex-thread-1"`, `"sessionId": "codex-session-1"`} {
		if !strings.Contains(structured, needle) {
			t.Fatalf("get agents -o json is missing %s:\n%s", needle, structured)
		}
	}
}

// TestAgentResumeIsUnchangedByAStoredSessionRef is acceptance criterion 4. The
// Phase that persists the conversation pointer must not start consuming it: an
// Offline Agent that now knows exactly which conversation it belongs to still
// ends at the same materialization stub, byte for byte, and a Running one is
// still refused with the same message.
func TestAgentResumeIsUnchangedByAStoredSessionRef(t *testing.T) {
	t.Parallel()

	ref := func() *coremetadata.AgentSessionRef {
		return &coremetadata.AgentSessionRef{
			Provider:   "codex",
			ObservedAt: sessionRefObservedAt,
			Codex:      &coremetadata.CodexSessionRef{ThreadID: "codex-thread-1", SessionID: "codex-session-1"},
		}
	}

	for _, test := range []struct {
		name string
		uid  string
		args []string
	}{
		{
			name: "an Offline agent still stops at the materialization boundary",
			uid:  "agt-beta-codex",
			args: []string{"resume", "codex", "--project", "beta"},
		},
		{
			name: "a Running agent is still refused",
			uid:  "agt-alpha-codex",
			args: []string{"resume", "codex", "--project", "alpha"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			// Run the identical invocation twice: once against the fixture as it
			// is, and once with a session ref recorded on the target Agent. The
			// two results must be indistinguishable.
			without := newFakeResourceStore(t)
			cmdWithout, aiWithout, usageWithout := newTestAgentCommand(t, without)
			outWithout, errWithout, runErrWithout := runRoute(t, cmdWithout, test.args...)

			with := newFakeResourceStore(t)
			setFixtureSessionRef(t, with, test.uid, ref())
			cmdWith, aiWith, usageWith := newTestAgentCommand(t, with)
			outWith, errWith, runErrWith := runRoute(t, cmdWith, test.args...)

			if outWithout != outWith || errWithout != errWith {
				t.Fatalf("streams diverged:\nwithout stdout=%q stderr=%q\nwith stdout=%q stderr=%q",
					outWithout, errWithout, outWith, errWith)
			}
			if (runErrWithout == nil) != (runErrWith == nil) {
				t.Fatalf("error presence diverged: without=%v with=%v", runErrWithout, runErrWith)
			}
			if runErrWithout != nil && runErrWithout.Error() != runErrWith.Error() {
				t.Fatalf("error text diverged:\nwithout=%q\nwith=%q", runErrWithout, runErrWith)
			}
			if runErrWithout == nil {
				t.Fatal("agent resume unexpectedly succeeded; the stub must still fail")
			}
			// The stored conversation must not have become an argv forwarded to
			// another handler either.
			for name, recorder := range map[string]*recordingArgv{
				"ai without": aiWithout, "usage without": usageWithout,
				"ai with": aiWith, "usage with": usageWith,
			} {
				if len(recorder.calls) != 0 {
					t.Fatalf("%s handler was reached: %#v", name, recorder.calls)
				}
			}
			// And it must not have been mutated by the read.
			if agent, ok := with.registry.Agent(test.uid); !ok || !agent.Status.SessionRef.SameConversation(ref()) {
				t.Fatal("agent resume disturbed the stored session ref")
			}
			if with.transactions != 0 {
				t.Fatalf("agent resume opened %d registry transactions, want 0", with.transactions)
			}
		})
	}
}
