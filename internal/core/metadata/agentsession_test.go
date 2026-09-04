package metadata

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// observedAt is the fixed hook-observation instant every session ref test uses.
var observedAt = time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)

func sessionRefMutator() Mutator {
	return Mutator{Now: func() time.Time { return observedAt }, NewUID: sequentialUIDs(), DirExists: dirSet{"/src/app": true}.exists}
}

// TestOneHookObservationFoldsOntoItsOwnProviderMember is the per-provider table
// that pins the union shape. The three providers deliberately produce three
// different field sets from the same four-field observation, which is the
// property a single flattened string could not express.
func TestOneHookObservationFoldsOntoItsOwnProviderMember(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		obs        AgentSessionObservation
		wantOK     bool
		wantRef    *AgentSessionRef
		wantFields [][2]string
		wantSummry string
	}{
		{
			name: "claude keeps its session id and transcript path",
			obs: AgentSessionObservation{
				Provider:       "claude",
				SessionID:      "claude-session-1",
				TranscriptPath: "/home/u/.claude/projects/x/claude-session-1.jsonl",
			},
			wantOK: true,
			wantRef: &AgentSessionRef{
				Provider:   "claude",
				ObservedAt: observedAt,
				Claude: &ClaudeSessionRef{
					SessionID:      "claude-session-1",
					TranscriptPath: "/home/u/.claude/projects/x/claude-session-1.jsonl",
				},
			},
			wantFields: [][2]string{
				{"SessionID", "claude-session-1"},
				{"TranscriptPath", "/home/u/.claude/projects/x/claude-session-1.jsonl"},
			},
			wantSummry: "claude:claude-session-1",
		},
		{
			name: "codex keeps a thread id beside its session id",
			obs: AgentSessionObservation{
				Provider:  "codex",
				ThreadID:  "codex-thread-1",
				SessionID: "codex-session-1",
			},
			wantOK: true,
			wantRef: &AgentSessionRef{
				Provider:   "codex",
				ObservedAt: observedAt,
				Codex:      &CodexSessionRef{ThreadID: "codex-thread-1", SessionID: "codex-session-1"},
			},
			wantFields: [][2]string{
				{"ThreadID", "codex-thread-1"},
				{"SessionID", "codex-session-1"},
			},
			wantSummry: "codex:codex-thread-1",
		},
		{
			name: "codex without a thread id still points at its session",
			obs: AgentSessionObservation{
				Provider:  "codex",
				SessionID: "codex-session-2",
			},
			wantOK: true,
			wantRef: &AgentSessionRef{
				Provider:   "codex",
				ObservedAt: observedAt,
				Codex:      &CodexSessionRef{SessionID: "codex-session-2"},
			},
			wantFields: [][2]string{{"SessionID", "codex-session-2"}},
			wantSummry: "codex:codex-session-2",
		},
		{
			name: "antigravity folds its single conversation id",
			obs: AgentSessionObservation{
				Provider:       "antigravity",
				ThreadID:       "conversation-1",
				SessionID:      "conversation-1",
				TranscriptPath: "/tmp/antigravity/conversation-1.log",
			},
			wantOK: true,
			wantRef: &AgentSessionRef{
				Provider:   "antigravity",
				ObservedAt: observedAt,
				Antigravity: &AntigravitySessionRef{
					ConversationID: "conversation-1",
					TranscriptPath: "/tmp/antigravity/conversation-1.log",
				},
			},
			wantFields: [][2]string{
				{"ConversationID", "conversation-1"},
				{"TranscriptPath", "/tmp/antigravity/conversation-1.log"},
			},
			wantSummry: "antigravity:conversation-1",
		},
		{
			name:   "an unknown provider records nothing",
			obs:    AgentSessionObservation{Provider: "selective", SessionID: "s-1"},
			wantOK: false,
		},
		{
			name:   "an empty provider records nothing",
			obs:    AgentSessionObservation{SessionID: "s-1"},
			wantOK: false,
		},
		{
			name:   "claude without a session id records nothing",
			obs:    AgentSessionObservation{Provider: "claude", TranscriptPath: "/tmp/t.jsonl"},
			wantOK: false,
		},
		{
			name:   "codex with neither id records nothing",
			obs:    AgentSessionObservation{Provider: "codex"},
			wantOK: false,
		},
		{
			name:   "antigravity without a conversation id records nothing",
			obs:    AgentSessionObservation{Provider: "antigravity", TranscriptPath: "/tmp/t.log"},
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, ok := NewAgentSessionRef(tc.obs, observedAt)
			if ok != tc.wantOK {
				t.Fatalf("ok = %t, want %t (ref=%#v)", ok, tc.wantOK, got)
			}
			if !tc.wantOK {
				if got != nil {
					t.Fatalf("a refused observation returned %#v, want nil", got)
				}
				return
			}
			if !reflect.DeepEqual(got, tc.wantRef) {
				t.Fatalf("ref = %s, want %s", mustJSON(t, got), mustJSON(t, tc.wantRef))
			}
			if fields := got.Fields(); !reflect.DeepEqual(fields, tc.wantFields) {
				t.Fatalf("fields = %#v, want %#v", fields, tc.wantFields)
			}
			if summary := got.Summary(); summary != tc.wantSummry {
				t.Fatalf("summary = %q, want %q", summary, tc.wantSummry)
			}
		})
	}
}

// TestATurnIDIsNeverStoredAsAConversationPointer pins the deliberate omission:
// the observation shape has no turn field at all, so no ingest caller can hand
// one over and no serialized ref can carry one.
func TestATurnIDIsNeverStoredAsAConversationPointer(t *testing.T) {
	t.Parallel()

	obsType := reflect.TypeFor[AgentSessionObservation]()
	for i := range obsType.NumField() {
		if name := obsType.Field(i).Name; name == "TurnID" || name == "Turn" {
			t.Fatalf("AgentSessionObservation carries %s; a turn addresses one turn, not the conversation", name)
		}
	}
	codexType := reflect.TypeFor[CodexSessionRef]()
	for i := range codexType.NumField() {
		if name := codexType.Field(i).Name; name == "TurnID" || name == "Turn" {
			t.Fatalf("CodexSessionRef carries %s; a durable pointer cannot be a per-turn value", name)
		}
	}
}

// TestARecordedSessionRefSurvivesTheManagedPaneBeingReleased is acceptance
// criterion 2 at the model layer: the Pane is deleted, the Agent goes Offline,
// paneRef is cleared, and the conversation pointer stays.
func TestARecordedSessionRefSurvivesTheManagedPaneBeingReleased(t *testing.T) {
	t.Parallel()

	m := sessionRefMutator()
	reg := &Registry{APIVersion: APIVersion, SchemaVersion: SchemaVersion}
	project, err := m.RegisterProject(reg, RegisterProjectOptions{Root: "/src/app", DefaultShell: "/bin/zsh", OperationID: "op-1"})
	if err != nil {
		t.Fatalf("register project: %v", err)
	}
	windowUID := project.Windows[0].Metadata.UID

	agent, err := m.CreateAgent(reg, windowUID, CreateAgentOptions{Provider: "claude", OperationID: "op-2"})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	pane, err := m.AttachAgentPane(reg, agent.Metadata.UID, BootstrapPane{Command: "claude", CWD: "/src/app"}, "op-3")
	if err != nil {
		t.Fatalf("attach agent pane: %v", err)
	}

	if _, changed, err := m.RecordAgentSessionRef(reg, agent.Metadata.UID, AgentSessionObservation{
		Provider:       "claude",
		SessionID:      "claude-session-1",
		TranscriptPath: "/home/u/.claude/x.jsonl",
	}); err != nil || !changed {
		t.Fatalf("record = (%t, %v), want (true, nil)", changed, err)
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("registry invalid after recording: %v", err)
	}

	released, err := m.ReleaseAgentPane(reg, agent.Metadata.UID, AgentExitNormal, "exited")
	if err != nil {
		t.Fatalf("release agent pane: %v", err)
	}
	if _, ok := reg.Pane(pane.Metadata.UID); ok {
		t.Fatal("the managed pane still exists; the release contract deletes it")
	}
	if released.Status.Phase != PhaseOffline || released.Status.PaneRef != "" {
		t.Fatalf("agent = %s/%q, want Offline with a cleared paneRef", released.Status.Phase, released.Status.PaneRef)
	}
	if got := released.Status.SessionRef.Summary(); got != "claude:claude-session-1" {
		t.Fatalf("session ref after release = %q, want the conversation to survive the Pane", got)
	}

	// The same must hold for an explicit phase transition, which also clears
	// paneRef.
	transitioned, err := m.TransitionAgent(reg, agent.Metadata.UID, PhaseFailed, "manual")
	if err != nil {
		t.Fatalf("transition agent: %v", err)
	}
	if got := transitioned.Status.SessionRef.Summary(); got != "claude:claude-session-1" {
		t.Fatalf("session ref after transition = %q, want it preserved", got)
	}
}

// TestReObservingTheSameConversationWritesNothing keeps a hook that fires on
// every turn from rewriting the registry file each time.
func TestReObservingTheSameConversationWritesNothing(t *testing.T) {
	t.Parallel()

	m := sessionRefMutator()
	reg, agentUID := agentFixture(t, m, "codex")

	obs := AgentSessionObservation{Provider: "codex", ThreadID: "t-1", SessionID: "s-1"}
	if _, changed, err := m.RecordAgentSessionRef(reg, agentUID, obs); err != nil || !changed {
		t.Fatalf("first record = (%t, %v), want (true, nil)", changed, err)
	}
	before := mustJSON(t, reg)
	if _, changed, err := m.RecordAgentSessionRef(reg, agentUID, obs); err != nil || changed {
		t.Fatalf("second record = (%t, %v), want (false, nil)", changed, err)
	}
	if after := mustJSON(t, reg); after != before {
		t.Fatalf("a re-observation mutated the registry:\nbefore=%s\nafter=%s", before, after)
	}

	// A different conversation is a real change and does replace the pointer:
	// the last observation wins.
	if _, changed, err := m.RecordAgentSessionRef(reg, agentUID, AgentSessionObservation{Provider: "codex", ThreadID: "t-2"}); err != nil || !changed {
		t.Fatalf("new conversation record = (%t, %v), want (true, nil)", changed, err)
	}
	agent, _ := reg.Agent(agentUID)
	if got := agent.Status.SessionRef.Summary(); got != "codex:t-2" {
		t.Fatalf("session ref = %q, want the newest observation", got)
	}
}

// TestRecordingASessionRefTouchesNoLifecycleField proves the observation write
// is not a lifecycle event: the phase, the transition timestamp, the reason,
// and the pane binding are all left exactly as they were.
func TestRecordingASessionRefTouchesNoLifecycleField(t *testing.T) {
	t.Parallel()

	m := sessionRefMutator()
	reg, agentUID := agentFixture(t, m, "claude")
	before, _ := reg.Agent(agentUID)
	wantPhase, wantAt, wantReason, wantPane := before.Status.Phase, before.Status.LastTransitionAt, before.Status.Reason, before.Status.PaneRef

	if _, _, err := m.RecordAgentSessionRef(reg, agentUID, AgentSessionObservation{Provider: "claude", SessionID: "s-1"}); err != nil {
		t.Fatalf("record: %v", err)
	}
	after, _ := reg.Agent(agentUID)
	if after.Status.Phase != wantPhase || !after.Status.LastTransitionAt.Equal(wantAt) || after.Status.Reason != wantReason || after.Status.PaneRef != wantPane {
		t.Fatalf("lifecycle changed: phase=%s at=%s reason=%q paneRef=%q", after.Status.Phase, after.Status.LastTransitionAt, after.Status.Reason, after.Status.PaneRef)
	}
}

// TestASessionRefIsRefusedWhenItContradictsTheAgentProvider stops a Claude hook
// from stamping a Codex conversation onto a Codex Agent.
func TestASessionRefIsRefusedWhenItContradictsTheAgentProvider(t *testing.T) {
	t.Parallel()

	m := sessionRefMutator()
	reg, agentUID := agentFixture(t, m, "codex")
	before := mustJSON(t, reg)

	_, changed, err := m.RecordAgentSessionRef(reg, agentUID, AgentSessionObservation{Provider: "claude", SessionID: "s-1"})
	if err == nil {
		t.Fatal("a cross-provider record was accepted")
	}
	if changed {
		t.Fatal("a refused record reported a change")
	}
	if after := mustJSON(t, reg); after != before {
		t.Fatalf("a refused record mutated the registry:\nbefore=%s\nafter=%s", before, after)
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("registry invalid after refusal: %v", err)
	}
}

// TestTwoAgentsMayPointAtTheSameConversation records the Phase 0 verdict:
// "one conversation <-> at most one live Agent" is deliberately NOT enforced.
// If this test ever has to be deleted, the docs that state the non-enforcement
// have to change with it.
func TestTwoAgentsMayPointAtTheSameConversation(t *testing.T) {
	t.Parallel()

	m := sessionRefMutator()
	reg := &Registry{APIVersion: APIVersion, SchemaVersion: SchemaVersion}
	project, err := m.RegisterProject(reg, RegisterProjectOptions{Root: "/src/app", DefaultShell: "/bin/zsh", OperationID: "op-1"})
	if err != nil {
		t.Fatalf("register project: %v", err)
	}
	windowUID := project.Windows[0].Metadata.UID

	obs := AgentSessionObservation{Provider: "claude", SessionID: "shared-conversation"}
	var uids []string
	for i := range 2 {
		agent, err := m.CreateAgent(reg, windowUID, CreateAgentOptions{Provider: "claude", OperationID: "op-agent"})
		if err != nil {
			t.Fatalf("create agent %d: %v", i, err)
		}
		if _, changed, err := m.RecordAgentSessionRef(reg, agent.Metadata.UID, obs); err != nil || !changed {
			t.Fatalf("record on agent %d = (%t, %v), want (true, nil)", i, changed, err)
		}
		uids = append(uids, agent.Metadata.UID)
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("two Agents pointing at one conversation must stay valid state: %v", err)
	}
	for _, uid := range uids {
		agent, _ := reg.Agent(uid)
		if got := agent.Status.SessionRef.Summary(); got != "claude:shared-conversation" {
			t.Fatalf("agent %s session ref = %q, want the shared conversation", uid, got)
		}
	}
}

// TestSessionRefValidationRejectsAStructurallyImpossibleUnion covers the
// invariants a hand-edited or foreign registry could violate.
func TestSessionRefValidationRejectsAStructurallyImpossibleUnion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ref  *AgentSessionRef
	}{
		{
			name: "unknown provider",
			ref:  &AgentSessionRef{Provider: "selective", Claude: &ClaudeSessionRef{SessionID: "s"}},
		},
		{
			name: "no member populated",
			ref:  &AgentSessionRef{Provider: "claude"},
		},
		{
			name: "two members populated",
			ref: &AgentSessionRef{
				Provider: "claude",
				Claude:   &ClaudeSessionRef{SessionID: "s"},
				Codex:    &CodexSessionRef{ThreadID: "t"},
			},
		},
		{
			name: "discriminator names a different member",
			ref:  &AgentSessionRef{Provider: "claude", Codex: &CodexSessionRef{ThreadID: "t"}},
		},
		{
			name: "populated member carries no conversation id",
			ref:  &AgentSessionRef{Provider: "claude", Claude: &ClaudeSessionRef{TranscriptPath: "/tmp/t.jsonl"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := sessionRefMutator()
			reg, agentUID := agentFixture(t, m, "claude")
			agent, _ := reg.Agent(agentUID)
			agent.Status.SessionRef = tc.ref
			if err := reg.Validate(); !errors.Is(err, ErrInvalidRegistry) {
				t.Fatalf("Validate = %v, want ErrInvalidRegistry", err)
			}
		})
	}
}

// TestAnAgentWithNoNormalizedProviderStillRecordsItsConversation keeps the
// cross-check from punishing an Agent whose provider spelling the registry
// never resolved.
func TestAnAgentWithNoNormalizedProviderStillRecordsItsConversation(t *testing.T) {
	t.Parallel()

	m := sessionRefMutator()
	reg, agentUID := agentFixture(t, m, "not-a-provider")
	agent, _ := reg.Agent(agentUID)
	if agent.Spec.Provider != "" {
		t.Fatalf("fixture spec.provider = %q, want the unresolved empty spelling", agent.Spec.Provider)
	}
	if _, changed, err := m.RecordAgentSessionRef(reg, agentUID, AgentSessionObservation{Provider: "claude", SessionID: "s-1"}); err != nil || !changed {
		t.Fatalf("record = (%t, %v), want (true, nil)", changed, err)
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// TestCloningARegistryDeepCopiesEverySessionRefMember stops two snapshots from
// aliasing one conversation record through the union pointers.
func TestCloningARegistryDeepCopiesEverySessionRefMember(t *testing.T) {
	t.Parallel()

	m := sessionRefMutator()
	reg, agentUID := agentFixture(t, m, "claude")
	if _, _, err := m.RecordAgentSessionRef(reg, agentUID, AgentSessionObservation{Provider: "claude", SessionID: "s-1", TranscriptPath: "/tmp/a.jsonl"}); err != nil {
		t.Fatalf("record: %v", err)
	}

	clone := reg.Clone()
	cloned, _ := clone.Agent(agentUID)
	original, _ := reg.Agent(agentUID)
	if cloned.Status.SessionRef == original.Status.SessionRef {
		t.Fatal("the clone shares the session ref pointer with the original")
	}
	if cloned.Status.SessionRef.Claude == original.Status.SessionRef.Claude {
		t.Fatal("the clone shares the Claude member pointer with the original")
	}
	cloned.Status.SessionRef.Claude.SessionID = "mutated"
	if original.Status.SessionRef.Claude.SessionID != "s-1" {
		t.Fatalf("mutating the clone changed the original to %q", original.Status.SessionRef.Claude.SessionID)
	}
}

// TestARegistryWrittenBeforeTheSessionRefExistedRoundTripsByteIdentically is
// acceptance criterion 3 at the model layer. The field is additive and
// omitempty, so an older document decodes with a nil ref, validates, migrates
// as a no-op at the current envelope, and re-encodes to the same bytes.
func TestARegistryWrittenBeforeTheSessionRefExistedRoundTripsByteIdentically(t *testing.T) {
	t.Parallel()

	reg, _ := agentFixture(t, sessionRefMutator(), "claude")
	data := []byte(mustJSON(t, reg.Normalize()) + "\n")
	if bytes.Contains(data, []byte("sessionRef")) {
		t.Fatal("the pre-field fixture already mentions sessionRef; it can no longer prove read compatibility")
	}

	var decoded Registry
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded.Agents) == 0 {
		t.Fatal("fixture carries no Agent")
	}
	for _, agent := range decoded.Agents {
		if agent.Status.SessionRef != nil {
			t.Fatalf("agent %q decoded a non-nil session ref from a document that has no such key", agent.Metadata.Name)
		}
		if !agent.Status.SessionRef.Empty() {
			t.Fatalf("agent %q reports a non-empty session ref", agent.Metadata.Name)
		}
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("a pre-field registry must still validate: %v", err)
	}

	action, err := ClassifySchemaVersion(decoded.SchemaVersion)
	if err != nil || action != SchemaCurrent {
		t.Fatalf("classify = (%s, %v), want (current, nil): the field is additive inside schemaVersion %d", action, err, SchemaVersion)
	}
	migrated, ran, err := MigrateRegistry(decoded)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if ran {
		t.Fatal("an additive status field must not require a migration step")
	}

	if got := mustJSON(t, migrated) + "\n"; got != string(data) {
		t.Fatalf("re-encode is not byte-identical:\n--- got ---\n%s\n--- want ---\n%s", got, data)
	}
}

// TestAgentSessionRefSerializationGolden freezes the on-disk shape of all three
// provider members in one document.
func TestAgentSessionRefSerializationGolden(t *testing.T) {
	t.Parallel()

	m := sessionRefMutator()
	reg := &Registry{APIVersion: APIVersion, SchemaVersion: SchemaVersion, UpdatedAt: observedAt}
	project, err := m.RegisterProject(reg, RegisterProjectOptions{Root: "/src/app", DefaultShell: "/bin/zsh", OperationID: "op-1"})
	if err != nil {
		t.Fatalf("register project: %v", err)
	}
	windowUID := project.Windows[0].Metadata.UID

	for _, obs := range []AgentSessionObservation{
		{Provider: "claude", SessionID: "claude-session-1", TranscriptPath: "/home/u/.claude/projects/app/claude-session-1.jsonl"},
		{Provider: "codex", ThreadID: "codex-thread-1", SessionID: "codex-session-1"},
		{Provider: "antigravity", ThreadID: "antigravity-conversation-1", SessionID: "antigravity-conversation-1"},
	} {
		agent, err := m.CreateAgent(reg, windowUID, CreateAgentOptions{Provider: obs.Provider, OperationID: "op-agent"})
		if err != nil {
			t.Fatalf("create %s agent: %v", obs.Provider, err)
		}
		if _, changed, err := m.RecordAgentSessionRef(reg, agent.Metadata.UID, obs); err != nil || !changed {
			t.Fatalf("record %s = (%t, %v), want (true, nil)", obs.Provider, changed, err)
		}
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("golden registry is invalid: %v", err)
	}

	got := mustJSON(t, reg.Normalize()) + "\n"
	for _, want := range []string{`"schemaVersion": 4`, `"claude": {`, `"sessionId": "claude-session-1"`, `"codex": {`, `"threadId": "codex-thread-1"`, `"antigravity": {`, `"conversationId": "antigravity-conversation-1"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("session ref serialization missing %s:\n%s", want, got)
		}
	}
	if strings.Contains(got, `"displayName"`) || strings.Contains(got, `"displayTitle"`) {
		t.Fatalf("schema v4 writer emitted removed presentation fields:\n%s", got)
	}

	// The golden decodes back into the same value, so the shape is not
	// write-only.
	var decoded Registry
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("decode serialized registry: %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("decoded serialized registry is invalid: %v", err)
	}
	if round := mustJSON(t, decoded) + "\n"; round != got {
		t.Fatalf("serialized registry does not round-trip:\n--- got ---\n%s\n--- want ---\n%s", round, got)
	}
}

// agentFixture builds a registry holding one Pending Agent for the provider.
func agentFixture(t *testing.T, m Mutator, provider string) (*Registry, string) {
	t.Helper()

	reg := &Registry{APIVersion: APIVersion, SchemaVersion: SchemaVersion}
	project, err := m.RegisterProject(reg, RegisterProjectOptions{Root: "/src/app", DefaultShell: "/bin/zsh", OperationID: "op-1"})
	if err != nil {
		t.Fatalf("register project: %v", err)
	}
	agent, err := m.CreateAgent(reg, project.Windows[0].Metadata.UID, CreateAgentOptions{Provider: provider, OperationID: "op-2"})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	return reg, agent.Metadata.UID
}
