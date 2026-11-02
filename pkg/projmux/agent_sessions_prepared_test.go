package projmux

import (
	"context"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/sessionhistory"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

var preparedSessionsAt = time.Date(2026, 11, 3, 1, 0, 0, 0, time.UTC)

// preparedSessionsRegistry is the graph fixture Registry with its Codex Agent
// holding a current thread, a Claude Agent beside it holding a current session
// and transcript path, and a shell Agent that has no session history.
func preparedSessionsRegistry(t *testing.T, change func(*metadata.Registry)) []byte {
	t.Helper()
	return mutatedGraphFixture(t, func(reg *metadata.Registry) {
		codex := &reg.Agents[0]
		codex.Status.SessionRef = &metadata.AgentSessionRef{Provider: "codex", ObservedAt: preparedSessionsAt, Codex: &metadata.CodexSessionRef{ThreadID: "thread-now"}}
		claude := codex.Clone()
		claude.Metadata.UID, claude.Metadata.Name, claude.Spec.Provider = "agent-02", "claude-two", "claude"
		claude.Status.SessionRef = &metadata.AgentSessionRef{Provider: "claude", ObservedAt: preparedSessionsAt, Claude: &metadata.ClaudeSessionRef{SessionID: "claude-now", TranscriptPath: "/never/open"}}
		shell := codex.Clone()
		shell.Metadata.UID, shell.Metadata.Name, shell.Spec.Provider, shell.Status.SessionRef = "agent-03", "shell-three", "shell", nil
		reg.Agents = append(reg.Agents, claude, shell)
		reg.NameReservations = append(reg.NameReservations,
			metadata.NameReservation{Kind: metadata.KindAgent, Name: "claude-two", UID: "agent-02"},
			metadata.NameReservation{Kind: metadata.KindAgent, Name: "shell-three", UID: "agent-03"})
		if change != nil {
			change(reg)
		}
	})
}

func preparedSessionsClient(t *testing.T) (*Client, string) {
	t.Helper()
	c, paths := graphFixtureClient(t, preparedSessionsRegistry(t, nil))
	last := preparedSessionsAt.Add(-time.Minute)
	removed, err := metadata.NewUID(metadata.KindProject)
	if err != nil {
		t.Fatal(err)
	}
	rows := []sessionhistory.Record{
		{AgentUID: "agent-01", Provider: "codex", SessionID: "thread-old", ObservedAt: last, LastRecordAt: &last, Source: sessionhistory.SourceEstimated},
		{AgentUID: "agent-01", Provider: "codex", SessionID: "thread-now", ObservedAt: last, Source: sessionhistory.SourceObserved, ProjectUID: "project-01", WindowUID: "window-01", AgentName: "agent-01"},
		{AgentUID: "agent-02", Provider: "claude", SessionID: "claude-now", ObservedAt: last, Source: sessionhistory.SourceObserved},
		{AgentUID: "agent-02", Provider: "codex", SessionID: "wrong-provider", ObservedAt: last, Source: sessionhistory.SourceObserved},
		{AgentUID: "agent-gone", Provider: "claude", SessionID: "removed-agent", ObservedAt: last, Source: sessionhistory.SourceObserved, ProjectUID: "project-01", WindowUID: "window-gone", AgentName: "gone"},
		{AgentUID: "agent-gone", Provider: "claude", SessionID: "unattributed", ObservedAt: last, Source: sessionhistory.SourceObserved},
		{AgentUID: "agent-gone", Provider: "claude", SessionID: "ambiguous", ObservedAt: last, Source: sessionhistory.SourceObserved, ProjectUID: "project-01"},
		{AgentUID: "agent-gone", Provider: "claude", SessionID: "ambiguous", ObservedAt: last, Source: sessionhistory.SourceObserved, ProjectUID: "project-other"},
		{AgentUID: "agent-gone", Provider: "claude", SessionID: "removed-project", ObservedAt: last, Source: sessionhistory.SourceObserved, ProjectUID: removed},
	}
	for _, row := range rows {
		if err := sessionhistory.Append(paths.StateDir, row); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.OpenFile(sessionhistory.Path(paths.StateDir), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString("corrupt\n"); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	return c, removed
}

func sameRefusal(t *testing.T, got, want error) {
	t.Helper()
	if (got == nil) != (want == nil) {
		t.Fatalf("error %v, want %v", got, want)
	}
	if got == nil {
		return
	}
	var g, w *Refusal
	if errors.As(got, &g) != errors.As(want, &w) || (g != nil && g.Code != w.Code) {
		t.Fatalf("refusal %v, want %v", got, want)
	}
}

// Both prepared reads return what the Registry-reading calls return for the
// same Registry and history: the same lists and the same refusals.
func TestListSessionsFromPreparedEqualsRegistryRead(t *testing.T) {
	ctx := context.Background()
	c, removed := preparedSessionsClient(t)
	prepared, _ := mustPrepare(t, c, nil)
	for _, agent := range []string{"agent-01", "uid:agent-01", "uid:agent-02", "claude-two", "shell-three", "uid:agent-03", "missing", "uid:agent-gone", "uid:", ""} {
		want, wantErr := c.ListAgentSessions(ctx, ListAgentSessionsRequest{Agent: agent})
		got, gotErr := c.ListAgentSessionsFromPrepared(ctx, prepared, agent)
		sameRefusal(t, gotErr, wantErr)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("agent %q: %+v, want %+v", agent, got, want)
		}
	}
	listed, err := c.ListAgentSessionsFromPrepared(ctx, prepared, "uid:agent-01")
	if err != nil || listed.CorruptLines != 1 || len(listed.Sessions) != 2 || listed.Sessions[0].LastRecordAt == nil || listed.Sessions[1].Source != core.SessionSourceCurrent {
		t.Fatalf("Agent join: %+v %v", listed, err)
	}
	_, err = c.ListAgentSessionsFromPrepared(ctx, prepared, "uid:agent-03")
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeSessionsProviderUnsupported {
		t.Fatalf("unsupported provider: %v", err)
	}
	for _, project := range []string{"uid:project-01", "alpha", "uid:" + removed, "uid:proj-malformed", "uid:agent-01", "missing", "uid:", ""} {
		want, wantErr := c.ListProjectSessions(ctx, ListProjectSessionsRequest{Project: project})
		got, gotErr := c.ListProjectSessionsFromPrepared(ctx, prepared, project)
		sameRefusal(t, gotErr, wantErr)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("project %q: %+v, want %+v", project, got, want)
		}
	}
	projected, err := c.ListProjectSessionsFromPrepared(ctx, prepared, "uid:project-01")
	if err != nil || projected.Unattributed != 1 || projected.Ambiguous != 1 || projected.CorruptLines != 1 || projected.ProjectName != "alpha" {
		t.Fatalf("Project counts: %+v %v", projected, err)
	}
	gone, err := c.ListProjectSessionsFromPrepared(ctx, prepared, "uid:"+removed)
	if err != nil || gone.ProjectName != "" || len(gone.Sessions) != 1 || gone.Sessions[0].SessionID != "removed-project" {
		t.Fatalf("removed Project: %+v %v", gone, err)
	}
}

// The handle stands in for the Registry: once prepared, a Registry file that
// changes or disappears does not change the lists, while the Registry-reading
// calls follow the file.
func TestListSessionsFromPreparedIgnoresLaterRegistryFile(t *testing.T) {
	ctx := context.Background()
	c, _ := preparedSessionsClient(t)
	prepared, _ := mustPrepare(t, c, nil)
	agentBefore, err := c.ListAgentSessionsFromPrepared(ctx, prepared, "uid:agent-02")
	if err != nil {
		t.Fatal(err)
	}
	projectBefore, err := c.ListProjectSessionsFromPrepared(ctx, prepared, "uid:project-01")
	if err != nil {
		t.Fatal(err)
	}
	path := store.PathFor(c.paths.StateDir)
	changed := preparedSessionsRegistry(t, func(reg *metadata.Registry) {
		reg.Agents = reg.Agents[:1]
		reg.Agents[0].Status.SessionRef = nil
	})
	if err := os.WriteFile(path, changed, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListAgentSessions(ctx, ListAgentSessionsRequest{Agent: "uid:agent-02"}); err == nil {
		t.Fatal("the Registry-reading call missed the changed file")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	agentAfter, err := c.ListAgentSessionsFromPrepared(ctx, prepared, "uid:agent-02")
	if err != nil || !reflect.DeepEqual(agentAfter, agentBefore) {
		t.Fatalf("Agent list followed the file: %+v %v", agentAfter, err)
	}
	projectAfter, err := c.ListProjectSessionsFromPrepared(ctx, prepared, "uid:project-01")
	if err != nil || !reflect.DeepEqual(projectAfter, projectBefore) {
		t.Fatalf("Project list followed the file: %+v %v", projectAfter, err)
	}
}

func TestListSessionsFromPreparedRefusesForeignHandles(t *testing.T) {
	ctx := context.Background()
	c, _ := preparedSessionsClient(t)
	other, _ := preparedSessionsClient(t)
	prepared, _ := mustPrepare(t, c, nil)
	foreign, _ := mustPrepare(t, other, nil)
	var nilHandle *PreparedRegistrySnapshot
	for name, tc := range map[string]struct {
		client   *Client
		prepared *PreparedRegistrySnapshot
	}{
		"nil": {c, nilHandle}, "zero": {c, &PreparedRegistrySnapshot{}}, "foreign root": {c, foreign}, "other client": {other, prepared},
	} {
		_, agentErr := tc.client.ListAgentSessionsFromPrepared(ctx, tc.prepared, "uid:agent-01")
		_, projectErr := tc.client.ListProjectSessionsFromPrepared(ctx, tc.prepared, "uid:project-01")
		for _, err := range []error{agentErr, projectErr} {
			var refusal *Refusal
			if !errors.As(err, &refusal) || refusal.Code != CodePreparedSnapshotInvalid {
				t.Fatalf("%s: %v", name, err)
			}
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.ListAgentSessionsFromPrepared(canceled, prepared, "uid:agent-01"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := c.ListProjectSessionsFromPrepared(canceled, prepared, "uid:project-01"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	sentinel := errors.New("captured paths")
	c.pathsErr = sentinel
	if _, err := c.ListAgentSessionsFromPrepared(ctx, prepared, "uid:agent-01"); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if _, err := c.ListProjectSessionsFromPrepared(ctx, prepared, "uid:project-01"); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}

// Results are caller-owned, the handle is never written, and concurrent calls
// with one handle agree (the race detector checks the sharing).
func TestListSessionsFromPreparedOwnershipAndConcurrency(t *testing.T) {
	ctx := context.Background()
	c, _ := preparedSessionsClient(t)
	prepared, _ := mustPrepare(t, c, nil)
	rows := prepared.Snapshot()
	first, err := c.ListAgentSessionsFromPrepared(ctx, prepared, "uid:agent-01")
	if err != nil {
		t.Fatal(err)
	}
	first.AgentName = "changed"
	*first.Sessions[0].LastRecordAt = time.Time{}
	first.Sessions[0].SessionID = "changed"
	again, err := c.ListAgentSessionsFromPrepared(ctx, prepared, "uid:agent-01")
	if err != nil || again.AgentName != "agent-01" || again.Sessions[0].SessionID != "thread-old" || again.Sessions[0].LastRecordAt.IsZero() {
		t.Fatalf("caller ownership: %+v %v", again, err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			got, err := c.ListAgentSessionsFromPrepared(ctx, prepared, "uid:agent-01")
			if err == nil && !reflect.DeepEqual(got, again) {
				err = errors.New("concurrent Agent list differs")
			}
			if err == nil {
				_, err = c.ListProjectSessionsFromPrepared(ctx, prepared, "uid:project-01")
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(prepared.Snapshot(), rows) {
		t.Fatal("a session list changed the handle")
	}
}

func sessionMergeTable() []struct {
	name    string
	history []sessionhistory.Record
	current *sessionhistory.Record
} {
	at := preparedSessionsAt
	last := at.Add(time.Hour)
	return []struct {
		name    string
		history []sessionhistory.Record
		current *sessionhistory.Record
	}{
		{"nil history, no current", nil, nil},
		{"current only", nil, &sessionhistory.Record{AgentUID: "a", Provider: "claude", SessionID: "s", ObservedAt: at, TranscriptPath: "/t", Source: sessionhistory.SourceCurrent}},
		{"history only, time order", []sessionhistory.Record{
			{AgentUID: "a", Provider: "codex", SessionID: "late", ObservedAt: at.Add(time.Hour), Source: sessionhistory.SourceObserved},
			{AgentUID: "a", Provider: "codex", SessionID: "early", ObservedAt: at, Source: sessionhistory.SourceEstimated, LastRecordAt: &last},
		}, nil},
		{"duplicate conversation merges", []sessionhistory.Record{
			{AgentUID: "a", Provider: "claude", SessionID: "s", ObservedAt: at, Source: sessionhistory.SourceEstimated, LastRecordAt: &last},
			{AgentUID: "a", Provider: "claude", SessionID: "s", ObservedAt: at.Add(time.Minute), TranscriptPath: "/t", Source: sessionhistory.SourceObserved, ProjectUID: "p", WindowUID: "w", AgentName: "n", AffiliationBasis: sessionhistory.BasisRegistry},
		}, &sessionhistory.Record{AgentUID: "a", Provider: "claude", SessionID: "s", ObservedAt: at.Add(2 * time.Minute), Source: sessionhistory.SourceCurrent}},
		{"affiliation survives an unaffiliated later row", []sessionhistory.Record{
			{AgentUID: "a", Provider: "codex", SessionID: "s", ObservedAt: at, Source: sessionhistory.SourceObserved, ProjectUID: "p", WindowUID: "w", AgentName: "n"},
			{AgentUID: "a", Provider: "codex", SessionID: "s", ObservedAt: at.Add(time.Minute), Source: sessionhistory.SourceObserved},
		}, nil},
		{"equal instants keep input order, current last", []sessionhistory.Record{
			{AgentUID: "a", Provider: "claude", SessionID: "one", ObservedAt: at, Source: sessionhistory.SourceObserved},
			{AgentUID: "a", Provider: "claude", SessionID: "two", ObservedAt: at, Source: sessionhistory.SourceObserved},
		}, &sessionhistory.Record{AgentUID: "a", Provider: "claude", SessionID: "three", ObservedAt: at, Source: sessionhistory.SourceCurrent}},
	}
}

// MergeAgentSessions is the internal Merge over the public row type.
func TestMergeAgentSessionsEqualsInternalMerge(t *testing.T) {
	for _, tc := range sessionMergeTable() {
		t.Run(tc.name, func(t *testing.T) {
			want := sessionhistory.Merge(tc.history, tc.current)
			history := make([]core.AgentSession, len(tc.history))
			for i, row := range tc.history {
				history[i] = publicAgentSession(row)
			}
			var current *core.AgentSession
			if tc.current != nil {
				row := publicAgentSession(*tc.current)
				current = &row
			}
			got := MergeAgentSessions(history, current)
			if got == nil || len(got) != len(want) {
				t.Fatalf("got %+v, want %+v", got, want)
			}
			for i := range want {
				if !reflect.DeepEqual(got[i], publicAgentSession(want[i])) {
					t.Fatalf("row %d: %+v, want %+v", i, got[i], want[i])
				}
			}
		})
	}
	// A merged row never shares a pointer with the input rows.
	last := preparedSessionsAt
	input := []core.AgentSession{{AgentUID: "a", Provider: "claude", SessionID: "s", ObservedAt: last, LastRecordAt: &last}}
	out := MergeAgentSessions(input, nil)
	*out[0].LastRecordAt = time.Time{}
	if input[0].LastRecordAt.IsZero() {
		t.Fatal("the result shares a pointer with its input")
	}
}

// The ordered join of CurrentAgentSession and MergeAgentSessions with the
// recorded history is the Agent list.
func TestCurrentAgentSessionEqualsInternalRecordFor(t *testing.T) {
	ctx := context.Background()
	c, _ := preparedSessionsClient(t)
	prepared, _ := mustPrepare(t, c, nil)
	recorded, err := c.ListRecordedSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := prepared.Snapshot()
	for _, agent := range snapshot.Agents {
		var history []core.AgentSession
		for _, row := range recorded.Sessions {
			if row.AgentUID == agent.Metadata.UID && row.Provider == agent.Spec.Provider {
				history = append(history, row)
			}
		}
		var current *core.AgentSession
		if row, ok := CurrentAgentSession(agent); ok {
			current = &row
		}
		if agent.Spec.Provider == "shell" {
			if current != nil {
				t.Fatalf("shell Agent has a current session: %+v", current)
			}
			continue
		}
		want, err := c.ListAgentSessionsFromPrepared(ctx, prepared, "uid:"+agent.Metadata.UID)
		if err != nil {
			t.Fatal(err)
		}
		if got := MergeAgentSessions(history, current); !reflect.DeepEqual(got, want.Sessions) {
			t.Fatalf("%s: join %+v, want %+v", agent.Metadata.Name, got, want.Sessions)
		}
	}

	claude := core.Agent{Metadata: core.ObjectMeta{UID: " agent-x "}, Status: core.AgentStatus{SessionRef: &core.AgentSessionRef{Provider: "claude", ObservedAt: preparedSessionsAt.In(time.FixedZone("x", 3600)), Claude: &core.ClaudeSessionRef{SessionID: " s ", TranscriptPath: "/t"}}}}
	internal := &metadata.AgentSessionRef{Provider: "claude", ObservedAt: claude.Status.SessionRef.ObservedAt, Claude: &metadata.ClaudeSessionRef{SessionID: " s ", TranscriptPath: "/t"}}
	wantRow, ok := sessionhistory.RecordFor(" agent-x ", internal, sessionhistory.SourceCurrent)
	if got, gotOK := CurrentAgentSession(claude); !ok || !gotOK || !reflect.DeepEqual(got, publicAgentSession(wantRow)) || got.Source != core.SessionSourceCurrent || got.AgentUID != "agent-x" || got.SessionID != "s" || got.ObservedAt.Location() != time.UTC {
		t.Fatalf("claude row: %+v %v", got, gotOK)
	}
	for _, tc := range []struct {
		name  string
		agent core.Agent
	}{
		{"no ref", core.Agent{Metadata: core.ObjectMeta{UID: "a"}}},
		{"no uid", core.Agent{Status: core.AgentStatus{SessionRef: &core.AgentSessionRef{Provider: "claude", Claude: &core.ClaudeSessionRef{SessionID: "s"}}}}},
		{"empty session", core.Agent{Metadata: core.ObjectMeta{UID: "a"}, Status: core.AgentStatus{SessionRef: &core.AgentSessionRef{Provider: "claude", Claude: &core.ClaudeSessionRef{SessionID: " "}}}}},
		{"claude without body", core.Agent{Metadata: core.ObjectMeta{UID: "a"}, Status: core.AgentStatus{SessionRef: &core.AgentSessionRef{Provider: "claude"}}}},
		{"codex empty", core.Agent{Metadata: core.ObjectMeta{UID: "a"}, Status: core.AgentStatus{SessionRef: &core.AgentSessionRef{Provider: "codex", Codex: &core.CodexSessionRef{}}}}},
		{"other provider", core.Agent{Metadata: core.ObjectMeta{UID: "a"}, Status: core.AgentStatus{SessionRef: &core.AgentSessionRef{Provider: "shell", Claude: &core.ClaudeSessionRef{SessionID: "s"}}}}},
		{"codex session id only", core.Agent{Metadata: core.ObjectMeta{UID: "a"}, Status: core.AgentStatus{SessionRef: &core.AgentSessionRef{Provider: "codex", Codex: &core.CodexSessionRef{SessionID: "s"}}}}},
	} {
		name, agent := tc.name, tc.agent
		if row, ok := CurrentAgentSession(agent); ok {
			t.Fatalf("%s: got %+v", name, row)
		}
	}
	codex, ok := CurrentAgentSession(core.Agent{Metadata: core.ObjectMeta{UID: "a"}, Status: core.AgentStatus{SessionRef: &core.AgentSessionRef{Provider: "codex", ObservedAt: preparedSessionsAt, Codex: &core.CodexSessionRef{ThreadID: "t"}}}})
	if !ok || codex.SessionID != "t" || codex.TranscriptPath != "" || codex.Provider != "codex" {
		t.Fatalf("codex row: %+v %v", codex, ok)
	}
}
