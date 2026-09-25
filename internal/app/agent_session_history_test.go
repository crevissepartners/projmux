package app

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/sessionhistory"
)

// claudeSessionRefWriters is the closed set of non-test functions that call
// RecordAgentSessionRef, keyed by repository-relative file and function name.
//
// Every one must turn the mutator's changed verdict into a history line with
// claudeSessionHistoryRecord and append it after its transaction commits.
//
// Intentionally excluded writers of status.sessionRef, because they are not
// Claude conversation changes:
//   - internal/core/metadata/nativebinding.go StageCodexEndpoint and
//     BindCodexActivation write Codex-only refs.
//   - internal/app/resource_controller.go restores a snapshot of the ref when
//     it rolls a failed transaction back; that is not a new conversation.
var claudeSessionRefWriters = []string{
	"internal/app/agent_session_ref.go:persistAgentSessionRef",
	"internal/app/agent_session_ref.go:persistManagedAgentInteractionWithActivationPolicy",
	"internal/app/create_intent.go:openIntentAgent",
}

// sessionHistoryAppendHelpers write a staged line after a commit.
var sessionHistoryAppendHelpers = []string{"recordClaudeSessionHistory", "recordIntentAgentSessionHistory"}

// TestClaudeSessionRefWritersRecordHistory is the closed-set guard of the
// Claude session history. It finds every non-test call of
// RecordAgentSessionRef under internal/ and cmd/ and requires the set of
// enclosing functions to be exactly claudeSessionRefWriters, each staging the
// history line and each reaching an append helper, itself or through every
// one of its callers.
func TestClaudeSessionRefWritersRecordHistory(t *testing.T) {
	root := filepath.Join("..", "..")
	funcs := map[string]*ast.FuncDecl{}
	var callers []string
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				key := rel + ":" + fn.Name.Name
				funcs[key] = fn
				if callsAny(fn, "RecordAgentSessionRef") {
					callers = append(callers, key)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", dir, err)
		}
	}
	sort.Strings(callers)
	want := slices.Clone(claudeSessionRefWriters)
	sort.Strings(want)
	if !slices.Equal(callers, want) {
		t.Fatalf("the callers of RecordAgentSessionRef changed:\n got  %v\n want %v\n"+
			"A function that writes status.sessionRef must also record the Claude session history: "+
			"stage the line with claudeSessionHistoryRecord inside the transaction, append it with "+
			"recordClaudeSessionHistory (or an equivalent post-commit helper) after the commit, and then "+
			"update claudeSessionRefWriters in this test.", callers, want)
	}
	for _, key := range want {
		fn := funcs[key]
		if !callsAny(fn, "claudeSessionHistoryRecord") {
			t.Errorf("%s calls RecordAgentSessionRef but never stages a history line with claudeSessionHistoryRecord", key)
		}
		if callsAny(fn, sessionHistoryAppendHelpers...) {
			continue
		}
		// The line is appended by the callers once their transaction commits.
		var reachedBy []string
		for callerKey, caller := range funcs {
			if callsAny(caller, fn.Name.Name) {
				reachedBy = append(reachedBy, callerKey)
				if !callsAny(caller, sessionHistoryAppendHelpers...) {
					t.Errorf("%s calls %s but never appends its staged session history line (%v)", callerKey, key, sessionHistoryAppendHelpers)
				}
			}
		}
		if len(reachedBy) == 0 {
			t.Errorf("%s stages a session history line that nothing appends", key)
		}
	}
}

// callsAny reports whether fn's body calls one of names, as a plain
// identifier or a selector.
func callsAny(fn *ast.FuncDecl, names ...string) bool {
	found := false
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || found {
			return !found
		}
		switch callee := call.Fun.(type) {
		case *ast.Ident:
			found = slices.Contains(names, callee.Name)
		case *ast.SelectorExpr:
			found = slices.Contains(names, callee.Sel.Name)
		}
		return !found
	})
	return found
}

func sessionHistoryHookPayload(sessionID string) string {
	return `{"hook_event_name":"UserPromptSubmit","session_id":"` + sessionID + `",` +
		`"transcript_path":"/home/u/.claude/projects/app/` + sessionID + `.jsonl","cwd":"/src/app"}`
}

// sessionHistoryStateDir is where the harness's hooks resolve the state dir:
// its temp HOME with no XDG override.
func sessionHistoryStateDir(t *testing.T, h *sessionRefHarness) string {
	t.Helper()
	dir, err := h.cmd.aiStateDir()
	if err != nil {
		t.Fatalf("aiStateDir: %v", err)
	}
	return dir
}

func readSessionHistory(t *testing.T, stateDir, agentUID string) sessionhistory.ReadResult {
	t.Helper()
	read, err := sessionhistory.Read(stateDir, agentUID)
	if err != nil {
		t.Fatalf("read session history: %v", err)
	}
	return read
}

func sessionHistoryAgentCommand(h *sessionRefHarness, stateDir string) *agentCommand {
	return &agentCommand{
		loadRegistry: func() (coremetadata.Registry, error) { return h.registry.Clone(), nil },
		store:        &resourceStore{stateDir: func() (string, error) { return stateDir, nil }},
	}
}

func TestClaudeHookMovingToAnotherConversationAppendsHistory(t *testing.T) {
	h := newSessionRefHarness(t, aiModeClaude)
	stateDir := sessionHistoryStateDir(t, h)

	h.ingest(t, []string{"claude-hook"}, sessionHistoryHookPayload("session-A"))
	later := sessionRefObservedAt.Add(time.Minute)
	h.cmd.now = func() time.Time { return later }
	h.ingest(t, []string{"claude-hook"}, sessionHistoryHookPayload("session-B"))

	read := readSessionHistory(t, stateDir, h.agentUID)
	if read.Corrupt != 0 || len(read.Records) != 2 {
		t.Fatalf("history = %#v, want two observed lines", read)
	}
	for i, want := range []string{"session-A", "session-B"} {
		record := read.Records[i]
		if record.SessionID != want || record.Source != sessionhistory.SourceObserved || record.Provider != aiModeClaude ||
			record.AgentUID != h.agentUID || !strings.HasSuffix(record.TranscriptPath, want+".jsonl") {
			t.Fatalf("line %d = %#v, want observed %s", i, record, want)
		}
	}
	if !read.Records[1].ObservedAt.Equal(later) {
		t.Fatalf("observedAt = %s, want the ref's ObservedAt %s", read.Records[1].ObservedAt, later)
	}

	var stdout, stderr bytes.Buffer
	if err := sessionHistoryAgentCommand(h, stateDir).Run([]string{"sessions", "list", "uid:" + h.agentUID}, &stdout, &stderr); err != nil {
		t.Fatalf("agent sessions list: %v (stderr=%s)", err, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "SESSION") ||
		!strings.Contains(lines[1], "session-A") || !strings.Contains(lines[1], "observed") ||
		!strings.Contains(lines[2], "session-B") || !strings.Contains(lines[2], "current") {
		t.Fatalf("table =\n%s", stdout.String())
	}
}

func TestClaudeHookReobservingTheSameConversationAppendsNothing(t *testing.T) {
	h := newSessionRefHarness(t, aiModeClaude)
	stateDir := sessionHistoryStateDir(t, h)
	for range 3 {
		h.ingest(t, []string{"claude-hook"}, sessionHistoryHookPayload("session-A"))
	}
	if read := readSessionHistory(t, stateDir, ""); len(read.Records) != 1 {
		t.Fatalf("history has %d lines after one conversation observed three times, want 1", len(read.Records))
	}
}

func TestCodexSessionRefChangesAppendNoHistory(t *testing.T) {
	h := newSessionRefHarness(t, aiModeCodex)
	stateDir := sessionHistoryStateDir(t, h)
	h.ingest(t, []string{"codex-hook"}, `{"hook_event_name":"UserPromptSubmit","thread_id":"thread-1","cwd":"/src/app"}`)
	h.ingest(t, []string{"codex-hook"}, `{"hook_event_name":"UserPromptSubmit","thread_id":"thread-2","cwd":"/src/app"}`)
	if ref := h.agent(t).Status.SessionRef; ref == nil || ref.Codex == nil || ref.Codex.ThreadID != "thread-2" {
		t.Fatalf("codex ref = %#v, want thread-2 recorded", ref)
	}
	if _, err := os.Stat(sessionhistory.Path(stateDir)); !os.IsNotExist(err) {
		t.Fatalf("a Codex conversation change touched the history file: %v", err)
	}
}

func TestSessionHistoryAppendFailureNeverFailsTheHook(t *testing.T) {
	h := newSessionRefHarness(t, aiModeClaude)
	stateDir := sessionHistoryStateDir(t, h)
	// A directory where the history file belongs: the append fails, the
	// ingest log beside it stays writable.
	if err := os.MkdirAll(sessionhistory.Path(stateDir), 0o700); err != nil {
		t.Fatal(err)
	}

	h.ingest(t, []string{"claude-hook"}, sessionHistoryHookPayload("session-A"))

	if ref := h.agent(t).Status.SessionRef; ref == nil || ref.Claude == nil || ref.Claude.SessionID != "session-A" {
		t.Fatalf("session ref = %#v, want the hook's conversation committed", ref)
	}
	logPath, err := h.cmd.aiIngestLogPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read ai-ingest.log: %v", err)
	}
	var failures []aiIngestLogEntry
	for _, line := range nonEmptyLines(string(data)) {
		var entry aiIngestLogEntry
		if json.Unmarshal([]byte(line), &entry) == nil && entry.Source == "session-history" {
			failures = append(failures, entry)
		}
	}
	if len(failures) != 1 || failures[0].Result != "error" || failures[0].Reason != aiIngestReasonSessionHistoryFailed || failures[0].SessionID != "session-A" {
		t.Fatalf("session-history log lines = %#v, want exactly one append failure", failures)
	}
}

func TestResumePickerCreateAppendsHistoryAfterCommitOrReportsOneLine(t *testing.T) {
	agentRef := &coremetadata.AgentSessionRef{
		Provider: aiModeClaude, ObservedAt: sessionRefObservedAt,
		Claude: &coremetadata.ClaudeSessionRef{SessionID: "picked", TranscriptPath: "/t/picked.jsonl"},
	}
	record, ok := claudeSessionHistoryRecord("agent-01", true, agentRef)
	if !ok {
		t.Fatal("a changed Claude ref staged no line")
	}
	if _, ok := claudeSessionHistoryRecord("agent-01", false, agentRef); ok {
		t.Fatal("an unchanged ref staged a line")
	}
	opened := intentAgentOpened{sessionHistory: record, sessionHistoryOK: true}

	stateDir := t.TempDir()
	c := &createCommand{store: &resourceStore{stateDir: func() (string, error) { return stateDir, nil }}}
	var stderr bytes.Buffer
	c.recordIntentAgentSessionHistory(opened, &stderr)
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want nothing", stderr.String())
	}
	if read := readSessionHistory(t, stateDir, "agent-01"); len(read.Records) != 1 || read.Records[0].SessionID != "picked" {
		t.Fatalf("history = %#v, want the picked conversation", read)
	}

	blocker := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	c.store.stateDir = func() (string, error) { return blocker, nil }
	c.recordIntentAgentSessionHistory(opened, &stderr)
	if got := stderr.String(); got != "agent session history not recorded: append-failed\n" {
		t.Fatalf("stderr = %q, want one not-recorded line", got)
	}
}

func TestAgentSessionsListWithOnlyARegistryRef(t *testing.T) {
	h := newSessionRefHarness(t, aiModeClaude)
	agent, _ := h.registry.Agent(h.agentUID)
	agent.Status.SessionRef = &coremetadata.AgentSessionRef{
		Provider: aiModeClaude, ObservedAt: sessionRefObservedAt,
		Claude: &coremetadata.ClaudeSessionRef{SessionID: "before-history", TranscriptPath: "/t/before.jsonl"},
	}
	stateDir := t.TempDir()

	var stdout, stderr bytes.Buffer
	if err := sessionHistoryAgentCommand(h, stateDir).Run([]string{"sessions", "list", "uid:" + h.agentUID, "-o", "json"}, &stdout, &stderr); err != nil {
		t.Fatalf("agent sessions list -o json: %v (stderr=%s)", err, stderr.String())
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("decode %s: %v", stdout.String(), err)
	}
	if got, want := sortedKeys(envelope), []string{"agentName", "agentUID", "corruptLines", "sessions"}; !slices.Equal(got, want) {
		t.Fatalf("envelope keys = %v, want %v", got, want)
	}
	var rows []map[string]any
	if err := json.Unmarshal(envelope["sessions"], &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("sessions = %v, want one current row", rows)
	}
	if got, want := sortedKeys(rows[0]), []string{"agentUID", "observedAt", "provider", "sessionId", "source", "transcriptPath"}; !slices.Equal(got, want) {
		t.Fatalf("row keys = %v, want %v", got, want)
	}
	want := map[string]any{
		"agentUID": h.agentUID, "provider": "claude", "sessionId": "before-history",
		"transcriptPath": "/t/before.jsonl", "observedAt": "2026-08-15T12:00:00Z", "source": "current",
	}
	for key, value := range want {
		if rows[0][key] != value {
			t.Fatalf("row[%s] = %v, want %v", key, rows[0][key], value)
		}
	}
	if _, err := os.Stat(sessionhistory.Path(stateDir)); !os.IsNotExist(err) {
		t.Fatalf("listing created the history file: %v", err)
	}
}

func TestAgentSessionsListRefusesANonClaudeAgentAndBadArgv(t *testing.T) {
	h := newSessionRefHarness(t, aiModeCodex)
	c := sessionHistoryAgentCommand(h, t.TempDir())
	for _, args := range [][]string{
		{"sessions", "list", "uid:" + h.agentUID},
		{"sessions"},
		{"sessions", "show", "uid:" + h.agentUID},
		{"sessions", "list"},
		{"sessions", "list", "uid:" + h.agentUID, "-o", "yaml"},
	} {
		var stdout, stderr bytes.Buffer
		err := c.Run(args, &stdout, &stderr)
		if err == nil || exitCodeOf(err) != 2 {
			t.Errorf("%v: err = %v (exit %d), want a usage error", args, err, exitCodeOf(err))
		}
		if stdout.Len() != 0 {
			t.Errorf("%v printed %q", args, stdout.String())
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
