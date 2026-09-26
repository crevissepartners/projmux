package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentapproval"
)

// permissionTestInput keeps its whitespace and key order on purpose: the
// terminal-answer close compares it canonically.
const permissionTestInput = `{"command": "rm -rf build", "description": "clean the build"}`

// permissionTestPayload is a Claude Code 2.1.283 PermissionRequest payload.
// extra is spliced in before session_id, for agent_id and agent_type.
func permissionTestPayload(mode, tool, input, extra string) string {
	return `{"hook_event_name":"PermissionRequest",` + extra + `"session_id":"sess-1","transcript_path":"/t/sess-1.jsonl","cwd":"/srv/alpha",` +
		`"scratchpad_dir":"/tmp/s","prompt_id":"p-1","permission_mode":"` + mode + `","tool_name":"` + tool + `","tool_input":` + input +
		`,"permission_suggestions":[{"type":"addRules","rules":[{"toolName":"` + tool + `"}],"behavior":"allow","destination":"localSettings"}]}`
}

type permissionFixture struct {
	*questionFixture
	approvals *agentapproval.Store
	// opened counts store opens; windowCalls and answeringCalls count setting
	// reads. answering is what the setting says; empty is way 1.
	opened         int
	windowCalls    int
	answering      config.AgentApprovalAnswering
	answeringCalls int
	mu             sync.Mutex
}

// newPermissionFixture is the question fixture's Registry, where
// agt-alpha-codex is a Running Claude Agent on pan-alpha-codex, with the
// approval store and setting wired into both the hook and `agent approval`.
func newPermissionFixture(t *testing.T, answering config.AgentApprovalAnswering) *permissionFixture {
	t.Helper()
	fixture := &permissionFixture{questionFixture: newQuestionFixture(t, false), answering: answering}
	fixture.approvals = agentapproval.NewStore(t.TempDir())
	fixture.command.approvalStore = func() (*agentapproval.Store, error) { return fixture.approvals, nil }
	fixture.command.approvalAnswering = func() config.AgentApprovalAnswering { return fixture.answering }
	return fixture
}

func (f *permissionFixture) hook(window time.Duration) claudePermissionHook {
	return claudePermissionHook{
		loadRegistry: f.resources.store().load,
		store: func() (*agentapproval.Store, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.opened++
			return f.approvals, nil
		},
		answering: func() config.AgentApprovalAnswering {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.answeringCalls++
			return config.NormalizeAgentApprovalAnswering(string(f.answering))
		},
		window: func() time.Duration {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.windowCalls++
			return window
		},
		poll:   10 * time.Millisecond,
		giveUp: 100 * time.Millisecond,
		newID:  agentapproval.NewID,
		now:    time.Now,
	}
}

// startPermissionHook runs hook in the background on payload and waits until
// it has recorded a new request and that request's requested audit line, then
// returns the request's id. Create writes the record before it appends the
// audit line, so a visible record alone does not yet mean an audited one.
func (f *permissionFixture) startPermissionHook(t *testing.T, ctx context.Context, hook claudePermissionHook, payload string) (string, <-chan string) {
	t.Helper()
	before := map[string]bool{}
	if records, err := f.approvals.List(questionTestAgent); err == nil {
		for _, record := range records {
			before[record.ID] = true
		}
	}
	done := make(chan string, 1)
	go func() {
		var stdout bytes.Buffer
		hook.run(ctx, []string{"--pane=" + questionTestPane}, strings.NewReader(payload), &stdout, &bytes.Buffer{})
		done <- stdout.String()
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if records, err := f.approvals.List(questionTestAgent); err == nil {
			for _, record := range records {
				if !before[record.ID] && permissionAuditRequested(f.approvals, record.ID) {
					return record.ID, done
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the hook never recorded its permission request and its requested audit line")
	return "", nil
}

// permissionAuditRequested reports whether the audit log holds a complete
// requested line for id. It is a tolerant poll check: a missing log, a line
// still being appended, or one that does not parse reads as not yet.
func permissionAuditRequested(store *agentapproval.Store, id string) bool {
	raw, err := os.ReadFile(store.AuditPath())
	if err != nil {
		return false
	}
	complete := raw[:bytes.LastIndexByte(raw, '\n')+1]
	for text := range bytes.SplitSeq(complete, []byte("\n")) {
		if len(text) == 0 {
			continue
		}
		var line agentapproval.AuditLine
		if json.Unmarshal(text, &line) != nil {
			return false
		}
		if line.Event == agentapproval.AuditRequested && line.RequestID == id {
			return true
		}
	}
	return false
}

func (f *permissionFixture) startDefault(t *testing.T, ctx context.Context) (string, <-chan string) {
	t.Helper()
	return f.startPermissionHook(t, ctx, f.hook(time.Minute), permissionTestPayload("default", "Bash", permissionTestInput, ""))
}

func readPermissionAudit(t *testing.T, store *agentapproval.Store) []agentapproval.AuditLine {
	t.Helper()
	file, err := os.Open(store.AuditPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var lines []agentapproval.AuditLine
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var line agentapproval.AuditLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	return lines
}

func permissionAuditEvents(lines []agentapproval.AuditLine) string {
	events := make([]string, 0, len(lines))
	for _, line := range lines {
		events = append(events, line.Event)
	}
	return strings.Join(events, ",")
}

// jsonKeys collects every object key anywhere in a JSON document.
func jsonKeys(t *testing.T, data []byte) map[string]bool {
	t.Helper()
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("decision %q is not JSON: %v", data, err)
	}
	keys := map[string]bool{}
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				keys[key] = true
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(value)
	return keys
}

// assertBareAllow holds D-3 on one allow output: exactly the documented bytes,
// and no key that would change the input or a permission rule.
func assertBareAllow(t *testing.T, out string) {
	t.Helper()
	if out != `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}`+"\n" {
		t.Fatalf("allow output = %q", out)
	}
	keys := jsonKeys(t, []byte(out))
	for _, forbidden := range []string{"updatedInput", "updatedPermissions", "permission_suggestions", "permissionSuggestions", "message", "interrupt"} {
		if keys[forbidden] {
			t.Fatalf("allow output carries %q: %s", forbidden, out)
		}
	}
}

// Acceptance 2 and 3, D-3: the two decisions the hook can ever print.
func TestClaudePermissionDecisionsNeverCarryInputOrRuleChanges(t *testing.T) {
	t.Parallel()

	assertBareAllow(t, string(claudePermissionAllowDecision))
	keys := jsonKeys(t, claudePermissionDenyDecision)
	if keys["updatedInput"] || keys["updatedPermissions"] {
		t.Fatalf("deny decision carries an input or rule change: %s", claudePermissionDenyDecision)
	}
	var deny struct {
		HookSpecificOutput struct {
			HookEventName string `json:"hookEventName"`
			Decision      struct {
				Behavior  string `json:"behavior"`
				Message   string `json:"message"`
				Interrupt *bool  `json:"interrupt"`
			} `json:"decision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(claudePermissionDenyDecision, &deny); err != nil ||
		deny.HookSpecificOutput.HookEventName != "PermissionRequest" || deny.HookSpecificOutput.Decision.Behavior != "deny" ||
		deny.HookSpecificOutput.Decision.Message != "denied by the operator in projmux" ||
		deny.HookSpecificOutput.Decision.Interrupt == nil || *deny.HookSpecificOutput.Decision.Interrupt {
		t.Fatalf("deny decision = %s (%v)", claudePermissionDenyDecision, err)
	}
}

// Acceptance 1: way 1 and every request the hook does not own read nothing
// past the payload and the Registry (and, for a confirmed projmux Claude
// Agent, the one setting), open no store, and print nothing.
func TestClaudePermissionHookWayOneReadsOnlyTheSettingAndOpensNothing(t *testing.T) {
	t.Parallel()

	payload := permissionTestPayload("default", "Bash", permissionTestInput, "")
	for _, test := range []struct {
		name    string
		way     config.AgentApprovalAnswering
		args    []string
		payload string
		// answeringReads is how often the setting may be read. Every case
		// that must not read it runs with the setting at projmux, so a read
		// would show up as an opened store too.
		answeringReads int
	}{
		{name: "way 1 on a projmux Claude Agent", way: config.AgentApprovalAnsweringClaude, args: []string{"--pane=" + questionTestPane}, payload: payload, answeringReads: 1},
		{name: "broken setting word", way: "allow-all", args: []string{"--pane=" + questionTestPane}, payload: payload, answeringReads: 1},
		{name: "outside projmux: unknown pane", way: config.AgentApprovalAnsweringProjmux, args: []string{"--pane=pan-nowhere"}, payload: payload},
		{name: "outside projmux: no pane and an unknown session", way: config.AgentApprovalAnsweringProjmux, args: []string{"--pane="}, payload: strings.Replace(payload, "sess-1", "sess-other", 1)},
		{name: "outside projmux: shell pane", way: config.AgentApprovalAnsweringProjmux, args: []string{"--pane=pan-alpha-zsh"}, payload: payload},
		{name: "bypassPermissions", way: config.AgentApprovalAnsweringProjmux, args: []string{"--pane=" + questionTestPane}, payload: permissionTestPayload("bypassPermissions", "Bash", permissionTestInput, "")},
		{name: "dontAsk", way: config.AgentApprovalAnsweringProjmux, args: []string{"--pane=" + questionTestPane}, payload: permissionTestPayload("dontAsk", "Bash", permissionTestInput, "")},
		{name: "another event", way: config.AgentApprovalAnsweringProjmux, args: []string{"--pane=" + questionTestPane}, payload: strings.Replace(payload, `"PermissionRequest"`, `"PreToolUse"`, 1)},
		{name: "no tool name", way: config.AgentApprovalAnsweringProjmux, args: []string{"--pane=" + questionTestPane}, payload: permissionTestPayload("default", "", permissionTestInput, "")},
		{name: "tool input not an object", way: config.AgentApprovalAnsweringProjmux, args: []string{"--pane=" + questionTestPane}, payload: permissionTestPayload("default", "Bash", `["x"]`, "")},
		{name: "malformed payload", way: config.AgentApprovalAnsweringProjmux, args: []string{"--pane=" + questionTestPane}, payload: `{"hook_event_name":`},
		{name: "oversized payload", way: config.AgentApprovalAnsweringProjmux, args: []string{"--pane=" + questionTestPane}, payload: strings.Replace(payload, "clean the build", strings.Repeat("x", claudePermissionPayloadLimit), 1)},
		{name: "oversized tool input", way: config.AgentApprovalAnsweringProjmux, args: []string{"--pane=" + questionTestPane}, payload: strings.Replace(payload, "clean the build", strings.Repeat("x", agentapproval.MaxToolInputBytes), 1)},
		{name: "positional argument", way: config.AgentApprovalAnsweringProjmux, args: []string{"extra"}, payload: payload},
		{name: "unknown flag", way: config.AgentApprovalAnsweringProjmux, args: []string{"--bogus"}, payload: payload},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newPermissionFixture(t, test.way)
			var stdout, stderr bytes.Buffer
			started := time.Now()
			fixture.hook(time.Minute).run(context.Background(), test.args, strings.NewReader(test.payload), &stdout, &stderr)
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Fatalf("hook took %s", elapsed)
			}
			if stdout.Len() != 0 || stderr.Len() != 0 || fixture.opened != 0 || fixture.windowCalls != 0 {
				t.Fatalf("stdout=%q stderr=%q store opened %d times, window read %d times", stdout.String(), stderr.String(), fixture.opened, fixture.windowCalls)
			}
			if fixture.answeringCalls != test.answeringReads {
				t.Fatalf("answering setting read %d times, want %d", fixture.answeringCalls, test.answeringReads)
			}
			if _, err := os.Stat(filepath.Dir(fixture.approvals.Path())); !os.IsNotExist(err) {
				t.Fatalf("store directory stat err = %v, want not exist", err)
			}
		})
	}
}

// Acceptance 1: a nil Registry reader, and a Registry that cannot be read,
// print nothing and read no setting.
func TestClaudePermissionHookWithoutARegistryReadsNoSetting(t *testing.T) {
	t.Parallel()

	for name, load := range map[string]func() (coremetadata.Registry, error){
		"nil":        nil,
		"unreadable": func() (coremetadata.Registry, error) { return coremetadata.Registry{}, errors.New("no registry") },
	} {
		fixture := newPermissionFixture(t, config.AgentApprovalAnsweringProjmux)
		hook := fixture.hook(time.Minute)
		hook.loadRegistry = load
		var stdout bytes.Buffer
		hook.run(context.Background(), []string{"--pane=" + questionTestPane}, strings.NewReader(permissionTestPayload("default", "Bash", permissionTestInput, "")), &stdout, &bytes.Buffer{})
		if stdout.Len() != 0 || fixture.answeringCalls != 0 || fixture.opened != 0 {
			t.Fatalf("%s: stdout=%q setting reads %d store opens %d", name, stdout.String(), fixture.answeringCalls, fixture.opened)
		}
	}
}

// Acceptance 2 and 6: an allow from the command line prints exactly the bare
// allow decision and is audited with the self-reported channel.
func TestClaudePermissionHookAllowFromTheCommandLine(t *testing.T) {
	t.Parallel()

	fixture := newPermissionFixture(t, config.AgentApprovalAnsweringProjmux)
	id, done := fixture.startDefault(t, context.Background())
	stdout, _, err := runRoute(t, fixture.command, "approval", "answer", "uid:"+questionTestAgent, id, "--allow", "--via", "web")
	if err != nil || stdout != id+" allowed for agent/codex\n" {
		t.Fatalf("answer stdout=%q err=%v", stdout, err)
	}
	assertBareAllow(t, waitHookOutput(t, done))
	record, _, _ := fixture.approvals.Get(id)
	if record.State != agentapproval.StateAllowed || record.Via != agentapproval.ViaWeb || record.ToolName != "Bash" ||
		record.PaneUID != questionTestPane || record.SessionID != "sess-1" || record.AgentType != "" {
		t.Fatalf("record = %+v", record)
	}
	lines := readPermissionAudit(t, fixture.approvals)
	if got := permissionAuditEvents(lines); got != "requested,allowed" {
		t.Fatalf("audit = %s", got)
	}
	if allowed := lines[1]; allowed.Via != "web" || allowed.Input != "rm -rf build" || allowed.AgentUID != questionTestAgent ||
		allowed.PaneUID != questionTestPane || allowed.SessionID != "sess-1" || allowed.DecidedAt.IsZero() || allowed.RequestedAt.IsZero() {
		t.Fatalf("allowed audit line = %+v", allowed)
	}
	info, err := os.Stat(fixture.approvals.AuditPath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("audit mode = %v (%v), want 0600", info.Mode().Perm(), err)
	}
}

// Acceptance 3: a deny prints the deny decision with the operator message.
func TestClaudePermissionHookDenyFromTheCommandLine(t *testing.T) {
	t.Parallel()

	fixture := newPermissionFixture(t, config.AgentApprovalAnsweringProjmux)
	id, done := fixture.startDefault(t, context.Background())
	stdout, _, err := runRoute(t, fixture.command, "approval", "answer", "uid:"+questionTestAgent, id, "--deny")
	if err != nil || stdout != id+" denied for agent/codex\n" {
		t.Fatalf("answer stdout=%q err=%v", stdout, err)
	}
	if got := waitHookOutput(t, done); got != string(claudePermissionDenyDecision) {
		t.Fatalf("deny output = %q", got)
	}
	record, _, _ := fixture.approvals.Get(id)
	if record.State != agentapproval.StateDenied || record.Via != agentapproval.ViaCLI {
		t.Fatalf("record = %+v", record)
	}
	if got := permissionAuditEvents(readPermissionAudit(t, fixture.approvals)); got != "requested,denied" {
		t.Fatalf("audit = %s", got)
	}
}

// Acceptance 11: a subagent's request is captured and carries its agent type in
// the record, the audit line, and both list projections.
func TestClaudePermissionHookCapturesSubagentRequestsWithTheirType(t *testing.T) {
	t.Parallel()

	fixture := newPermissionFixture(t, config.AgentApprovalAnsweringProjmux)
	id, done := fixture.startPermissionHook(t, context.Background(), fixture.hook(time.Minute),
		permissionTestPayload("default", "mcp__db__query", `{"sql":"drop table x"}`, `"agent_id":"sub-1","agent_type":"Explore",`))
	record, _, _ := fixture.approvals.Get(id)
	if record.AgentType != "Explore" || record.ToolName != "mcp__db__query" {
		t.Fatalf("record = %+v", record)
	}
	text, _, err := runRoute(t, fixture.command, "approval", "list", "uid:"+questionTestAgent)
	if err != nil || !strings.Contains(text, id+"\twaiting\ttool mcp__db__query\tsubagent Explore\tcreated ") ||
		!strings.Contains(text, `  input: {"sql":"drop table x"}`) || !strings.Contains(text, "answered in Claude Code's own prompt") {
		t.Fatalf("list text = %q, %v", text, err)
	}
	raw, _, err := runRoute(t, fixture.command, "approval", "list", "uid:"+questionTestAgent, "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var listed agentPermissionList
	if err := json.Unmarshal([]byte(raw), &listed); err != nil || listed.Answering != "projmux" || len(listed.Requests) != 1 ||
		listed.Requests[0].AgentType != "Explore" || string(listed.Requests[0].ToolInput) != `{"sql":"drop table x"}` ||
		!listed.Requests[0].Deadline.After(listed.Requests[0].CreatedAt) {
		t.Fatalf("list json = %s (%v)", raw, err)
	}
	if lines := readPermissionAudit(t, fixture.approvals); len(lines) != 1 || lines[0].AgentType != "Explore" || lines[0].Input != "keys: sql" {
		t.Fatalf("audit = %+v", lines)
	}
	if _, _, err := runRoute(t, fixture.command, "approval", "answer", "uid:"+questionTestAgent, id, "--allow"); err != nil {
		t.Fatal(err)
	}
	assertBareAllow(t, waitHookOutput(t, done))
}

// Acceptance 4: every way the wait can end other than an answer prints
// nothing, and never an allow.
func TestClaudePermissionHookNegativesPrintNothing(t *testing.T) {
	t.Parallel()

	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		window time.Duration
		// adjust changes the hook before it runs.
		adjust    func(*permissionFixture, *claudePermissionHook)
		cancel    bool
		wantState agentapproval.State
		stderr    string
	}{
		{name: "no answer until the window ends", window: 150 * time.Millisecond, wantState: agentapproval.StateExpired},
		{name: "cancel", window: time.Minute, cancel: true, wantState: agentapproval.StateClosed},
		{name: "store open error", window: time.Minute, adjust: func(_ *permissionFixture, h *claudePermissionHook) {
			h.store = func() (*agentapproval.Store, error) { return nil, errors.New("no state dir") }
		}, stderr: "approval store unavailable"},
		{name: "store create error", window: time.Minute, adjust: func(_ *permissionFixture, h *claudePermissionHook) {
			h.store = func() (*agentapproval.Store, error) {
				return agentapproval.NewStoreAt(filepath.Join(blocker, "agent-approvals", "requests.json")), nil
			}
		}, stderr: "permission request not recorded"},
		{name: "store read error past give-up", window: 150 * time.Millisecond, adjust: func(f *permissionFixture, h *claudePermissionHook) {
			// The store stays unreadable from the first poll on, so neither
			// the reads nor the deadline Settle can see the record.
			h.readRecord = func(store *agentapproval.Store, id string) (agentapproval.Record, bool, error) {
				_ = os.WriteFile(store.Path(), []byte("garbage"), 0o600)
				return agentapproval.Record{}, false, errors.New("unreadable")
			}
		}},
		{name: "record vanished", window: time.Minute, adjust: func(f *permissionFixture, h *claudePermissionHook) {
			h.readRecord = func(*agentapproval.Store, string) (agentapproval.Record, bool, error) {
				return agentapproval.Record{}, false, nil
			}
		}, wantState: agentapproval.StateClosed},
		{name: "no id", window: time.Minute, adjust: func(_ *permissionFixture, h *claudePermissionHook) {
			h.newID = func() (string, error) { return "", errors.New("no entropy") }
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newPermissionFixture(t, config.AgentApprovalAnsweringProjmux)
			hook := fixture.hook(test.window)
			if test.adjust != nil {
				test.adjust(fixture, &hook)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.cancel {
				time.AfterFunc(100*time.Millisecond, cancel)
			}
			var stdout, stderr bytes.Buffer
			done := make(chan struct{})
			go func() {
				defer close(done)
				hook.run(ctx, []string{"--pane=" + questionTestPane}, strings.NewReader(permissionTestPayload("default", "Bash", permissionTestInput, "")), &stdout, &stderr)
			}()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("the hook did not return")
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), test.stderr) {
				t.Fatalf("stdout=%q stderr=%q, want no decision and %q", stdout.String(), stderr.String(), test.stderr)
			}
			if test.wantState == "" {
				return
			}
			records, err := fixture.approvals.List(questionTestAgent)
			if err != nil || len(records) != 1 || records[0].State != test.wantState {
				t.Fatalf("records = %+v (%v), want one %s", records, err, test.wantState)
			}
			_, _, err = runRoute(t, fixture.command, "approval", "answer", "uid:"+questionTestAgent, records[0].ID, "--allow")
			if err == nil {
				t.Fatal("a late answer was accepted")
			}
		})
	}
}

// Acceptance 4: a cancellation refuses a later answer as not pending.
func TestClaudePermissionHookCanceledRefusesALateAnswer(t *testing.T) {
	t.Parallel()

	fixture := newPermissionFixture(t, config.AgentApprovalAnsweringProjmux)
	ctx, cancel := context.WithCancel(context.Background())
	id, done := fixture.startDefault(t, ctx)
	cancel()
	if got := waitHookOutput(t, done); got != "" {
		t.Fatalf("canceled hook printed %q", got)
	}
	_, _, err := runRoute(t, fixture.command, "approval", "answer", "uid:"+questionTestAgent, id, "--allow")
	if err == nil || !strings.Contains(err.Error(), "(permission-not-pending)") {
		t.Fatalf("late answer err = %v, want permission-not-pending", err)
	}
	if got := permissionAuditEvents(readPermissionAudit(t, fixture.approvals)); got != "requested,closed,refused" {
		t.Fatalf("audit = %s", got)
	}
}

// Acceptance 4 and 7: an expired request is refused and was held for exactly
// the window read for it.
func TestClaudePermissionHookExpiryRefusesALateAnswer(t *testing.T) {
	t.Parallel()

	fixture := newPermissionFixture(t, config.AgentApprovalAnsweringProjmux)
	id, done := fixture.startPermissionHook(t, context.Background(), fixture.hook(200*time.Millisecond), permissionTestPayload("default", "Bash", permissionTestInput, ""))
	if got := waitHookOutput(t, done); got != "" {
		t.Fatalf("expired hook printed %q", got)
	}
	record, _, _ := fixture.approvals.Get(id)
	if record.State != agentapproval.StateExpired || record.Deadline.Sub(record.CreatedAt) != 200*time.Millisecond {
		t.Fatalf("record = %s window %s", record.State, record.Deadline.Sub(record.CreatedAt))
	}
	_, _, err := runRoute(t, fixture.command, "approval", "answer", "uid:"+questionTestAgent, id, "--deny")
	if err == nil || !strings.Contains(err.Error(), "(permission-expired)") {
		t.Fatalf("late answer err = %v, want permission-expired", err)
	}
	if got := permissionAuditEvents(readPermissionAudit(t, fixture.approvals)); got != "requested,expired,refused" {
		t.Fatalf("audit = %s", got)
	}
}

// Acceptance 7: the window is read for every request, so a changed window
// applies to the next one without re-integrating.
func TestClaudePermissionHookReadsTheWindowForEveryRequest(t *testing.T) {
	t.Parallel()

	fixture := newPermissionFixture(t, config.AgentApprovalAnsweringProjmux)
	for i, window := range []time.Duration{time.Minute, 2 * time.Minute} {
		ctx, cancel := context.WithCancel(context.Background())
		id, done := fixture.startPermissionHook(t, ctx, fixture.hook(window), permissionTestPayload("default", "Bash", permissionTestInput, ""))
		record, _, _ := fixture.approvals.Get(id)
		if record.Deadline.Sub(record.CreatedAt) != window || fixture.windowCalls != i+1 {
			t.Fatalf("request %d window = %s after %d reads, want %s", i, record.Deadline.Sub(record.CreatedAt), fixture.windowCalls, window)
		}
		cancel()
		waitHookOutput(t, done)
	}

	paths := config.DefaultPaths(t.TempDir(), "")
	for content, want := range map[string]time.Duration{"": 900 * time.Second, "120": 120 * time.Second, "unlimited": 900 * time.Second, "59": 900 * time.Second, "3600": time.Hour} {
		if content != "" {
			if err := config.SaveAgentApprovalWindowSecondsFile(paths.AgentApprovalWindowSecondsFile(), 900); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(paths.AgentApprovalWindowSecondsFile(), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if got := claudePermissionWindowFromPaths(paths); got != want {
			t.Fatalf("window file %q = %s, want %s", content, got, want)
		}
		_ = os.Remove(paths.AgentApprovalWindowSecondsFile())
	}
}

// Acceptance 5: of two racing answers the first wins, the second is refused as
// permission-not-pending, and the hook prints the winner's decision.
func TestClaudePermissionAnswerFirstWins(t *testing.T) {
	t.Parallel()

	fixture := newPermissionFixture(t, config.AgentApprovalAnsweringProjmux)
	id, done := fixture.startDefault(t, context.Background())
	// Each racer reads its own Registry snapshot; the fake Registry store is
	// not safe for concurrent use, the approval store is what races.
	registry, err := fixture.resources.store().load()
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i, verdict := range []string{"--allow", "--deny"} {
		racer := &agentCommand{
			loadRegistry:      func() (coremetadata.Registry, error) { return registry.Clone(), nil },
			approvalStore:     fixture.command.approvalStore,
			approvalAnswering: fixture.command.approvalAnswering,
		}
		wg.Go(func() {
			_, _, results[i] = runRoute(t, racer, "approval", "answer", "uid:"+questionTestAgent, id, verdict)
		})
	}
	wg.Wait()
	winner := -1
	for i, err := range results {
		switch {
		case err == nil:
			if winner >= 0 {
				t.Fatal("both answers were accepted")
			}
			winner = i
		case !strings.Contains(err.Error(), "(permission-not-pending)"):
			t.Fatalf("losing answer err = %v, want permission-not-pending", err)
		}
	}
	if winner < 0 {
		t.Fatalf("no answer was accepted: %v", results)
	}
	got := waitHookOutput(t, done)
	if winner == 0 {
		assertBareAllow(t, got)
	} else if got != string(claudePermissionDenyDecision) {
		t.Fatalf("hook output = %q, want the deny decision", got)
	}
	if _, _, err := runRoute(t, fixture.command, "approval", "answer", "uid:"+questionTestAgent, id, "--allow"); err == nil || !strings.Contains(err.Error(), "(permission-not-pending)") {
		t.Fatalf("third answer err = %v", err)
	}
}

// Acceptance 10: a PostToolUse for the request the terminal already allowed
// closes exactly that one waiting record; the hook exits with no output and a
// later answer is refused.
func TestClaudePermissionAnsweredInTerminalClosesTheOneMatch(t *testing.T) {
	t.Parallel()

	fixture := newPermissionFixture(t, config.AgentApprovalAnsweringProjmux)
	id, done := fixture.startDefault(t, context.Background())
	opens := 0
	open := func() *agentapproval.Store { opens++; return fixture.approvals }
	answering := func() config.AgentApprovalAnswering { return fixture.answering }
	for name, test := range map[string]struct{ session, tool, input string }{
		"different input":   {session: "sess-1", tool: "Bash", input: `{"command":"rm -rf dist","description":"clean the build"}`},
		"different tool":    {session: "sess-1", tool: "Write", input: permissionTestInput},
		"different session": {session: "sess-2", tool: "Bash", input: permissionTestInput},
	} {
		closeClaudePermissionAnsweredInTerminal(answering, open, test.session, test.tool, json.RawMessage(test.input))
		if record, _, _ := fixture.approvals.Get(id); record.State != agentapproval.StateWaiting {
			t.Fatalf("%s closed the record: %s", name, record.State)
		}
	}
	// The same input spelled differently is the same tool call.
	closeClaudePermissionAnsweredInTerminal(answering, open, "sess-1", "Bash", json.RawMessage(`{"description":"clean the build","command":"rm -rf build"}`))
	if got := waitHookOutput(t, done); got != "" {
		t.Fatalf("hook printed %q after the terminal answer", got)
	}
	if record, _, _ := fixture.approvals.Get(id); record.State != agentapproval.StateClosed {
		t.Fatalf("state = %s, want closed", record.State)
	}
	_, _, err := runRoute(t, fixture.command, "approval", "answer", "uid:"+questionTestAgent, id, "--allow")
	if err == nil || !strings.Contains(err.Error(), "(permission-not-pending)") {
		t.Fatalf("answer after the terminal answer err = %v, want permission-not-pending", err)
	}
	lines := readPermissionAudit(t, fixture.approvals)
	if got := permissionAuditEvents(lines); got != "requested,closed,refused" || lines[1].Reason != "answered-in-terminal" || lines[1].RequestID != id {
		t.Fatalf("audit = %+v", lines)
	}
	if opens != 4 {
		t.Fatalf("store opened %d times, want once per PostToolUse in way projmux", opens)
	}
}

// Acceptance 10: two identical waiting requests are ambiguous, so the
// terminal-answer close closes neither.
func TestClaudePermissionAnsweredInTerminalLeavesTwoMatchesWaiting(t *testing.T) {
	t.Parallel()

	fixture := newPermissionFixture(t, config.AgentApprovalAnsweringProjmux)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first, firstDone := fixture.startDefault(t, ctx)
	second, secondDone := fixture.startDefault(t, ctx)
	closeClaudePermissionAnsweredInTerminal(func() config.AgentApprovalAnswering { return fixture.answering },
		func() *agentapproval.Store { return fixture.approvals }, "sess-1", "Bash", json.RawMessage(permissionTestInput))
	for _, id := range []string{first, second} {
		if record, _, _ := fixture.approvals.Get(id); record.State != agentapproval.StateWaiting {
			t.Fatalf("%s = %s, want waiting", id, record.State)
		}
	}
	cancel()
	waitHookOutput(t, firstDone)
	waitHookOutput(t, secondDone)
}

// Acceptance 10: in way 1 the close reads the setting and never opens the
// store; with no store file it creates nothing.
func TestClaudePermissionAnsweredInTerminalCheapPath(t *testing.T) {
	t.Parallel()

	fixture := newPermissionFixture(t, config.AgentApprovalAnsweringClaude)
	opens := 0
	open := func() *agentapproval.Store { opens++; return fixture.approvals }
	closeClaudePermissionAnsweredInTerminal(func() config.AgentApprovalAnswering { return fixture.answering }, open, "sess-1", "Bash", json.RawMessage(permissionTestInput))
	if opens != 0 {
		t.Fatalf("way 1 opened the store %d times", opens)
	}
	fixture.answering = config.AgentApprovalAnsweringProjmux
	closeClaudePermissionAnsweredInTerminal(func() config.AgentApprovalAnswering { return fixture.answering }, open, "sess-1", "Bash", json.RawMessage(permissionTestInput))
	if _, err := os.Stat(filepath.Dir(fixture.approvals.Path())); !os.IsNotExist(err) {
		t.Fatalf("no store file: store directory stat err = %v, want not exist", err)
	}
	for name, test := range map[string]struct{ session, tool string }{"no session": {tool: "Bash"}, "no tool": {session: "sess-1"}} {
		before := opens
		closeClaudePermissionAnsweredInTerminal(func() config.AgentApprovalAnswering { return fixture.answering }, open, test.session, test.tool, json.RawMessage(permissionTestInput))
		if opens != before {
			t.Fatalf("%s opened the store", name)
		}
	}
}

// Acceptance 8 and 10: the PostToolUse and PostToolUseFailure ingest hands the
// raw tool input to the close, and the addition changes nothing the ingest
// did before, even when it panics.
func TestClaudePostToolUseIngestIsUnchangedByThePermissionClose(t *testing.T) {
	t.Parallel()

	type outcome struct {
		Err      string
		Commands []recordedAICommand
		Loads    int
		Records  []aiIngestLogEntry
	}
	run := func(t *testing.T, event string, seam func(string, string, json.RawMessage)) outcome {
		f := newClaudeQuietHookFixture(t, true)
		f.cmd.permissionAnsweredInTerminal = seam
		payload := `{"hook_event_name":"` + event + `","session_id":"` + claudeQuietHookSession + `","cwd":"` + claudeQuietHookCWD +
			`","transcript_path":"` + claudeQuietHookTranscript + `","tool_name":"Bash","tool_input":` + permissionTestInput + `}`
		loads := f.loads
		err := f.cmd.ingestClaudeHook([]byte(payload), f.explicitPane)
		got := outcome{Commands: cmdRecorder(f.cmd).commands, Loads: f.loads - loads, Records: claudeQuietHookLogRecords(t, f.cmd)}
		if err != nil {
			got.Err = err.Error()
		}
		return got
	}
	for _, event := range []string{"PostToolUse", "PostToolUseFailure", "PermissionDenied"} {
		t.Run(event, func(t *testing.T) {
			t.Parallel()
			baseline := run(t, event, nil)
			var calls []string
			recording := run(t, event, func(session, tool string, input json.RawMessage) {
				canonical, _ := agentapproval.CanonicalInput(input)
				calls = append(calls, session+"|"+tool+"|"+canonical)
			})
			panicking := run(t, event, func(string, string, json.RawMessage) { panic("close bug") })
			for name, got := range map[string]outcome{"recording": recording, "panicking": panicking} {
				gotJSON, _ := json.Marshal(got)
				wantJSON, _ := json.Marshal(baseline)
				if !bytes.Equal(gotJSON, wantJSON) {
					t.Fatalf("%s seam changed the ingest:\n got %s\nwant %s", name, gotJSON, wantJSON)
				}
			}
			want := []string{claudeQuietHookSession + `|Bash|{"command":"rm -rf build","description":"clean the build"}`}
			if event == "PermissionDenied" {
				want = nil
			}
			if strings.Join(calls, "\n") != strings.Join(want, "\n") {
				t.Fatalf("close calls = %q, want %q", calls, want)
			}
		})
	}
}

// Acceptance 9: capturing permission requests changes neither the Claude
// coordination protocol version nor the Registry schema version.
func TestClaudePermissionCaptureLeavesProtocolVersionsAlone(t *testing.T) {
	t.Parallel()

	if claudeCoordinationVersion != 5 || coremetadata.SchemaVersion != 4 {
		t.Fatalf("claudeCoordinationVersion = %d, SchemaVersion = %d; want 5 and 4", claudeCoordinationVersion, coremetadata.SchemaVersion)
	}
}

// The CLI: usage errors, the answering-off and provider refusals, and no write
// on any refusal.
func TestAgentApprovalListAndAnswerRefusals(t *testing.T) {
	t.Parallel()

	fixture := newPermissionFixture(t, config.AgentApprovalAnsweringProjmux)
	for _, test := range []struct {
		args []string
		want string
	}{
		{args: []string{"approval"}, want: "agent approval requires review, list, answer"},
		{args: []string{"approval", "bogus"}, want: "agent approval requires review, list, answer"},
		{args: []string{"approval", "answer", "uid:" + questionTestAgent, "permission-0000000000000001"}, want: "requires exactly one of --allow or --deny"},
		{args: []string{"approval", "answer", "uid:" + questionTestAgent, "permission-0000000000000001", "--allow", "--deny"}, want: "requires exactly one of --allow or --deny"},
		{args: []string{"approval", "answer", "uid:" + questionTestAgent, "permission-0000000000000001", "--allow", "--via", "email"}, want: `unknown --via "email"`},
		{args: []string{"approval", "answer", "uid:" + questionTestAgent, "--allow"}, want: "requires <agent-ref> <request-id>"},
		{args: []string{"approval", "list"}, want: "requires <agent-ref>"},
		{args: []string{"approval", "list", "uid:" + questionTestAgent, "-o", "yaml"}, want: `unsupported output "yaml"`},
	} {
		_, _, err := runRoute(t, fixture.command, test.args...)
		if err == nil || !IsUsageError(err) || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("%q err = %v, want a usage error containing %q", test.args, err, test.want)
		}
	}
	if _, err := os.Stat(filepath.Dir(fixture.approvals.Path())); !os.IsNotExist(err) {
		t.Fatalf("a usage error touched the store: %v", err)
	}

	for _, verb := range [][]string{{"list", "uid:agt-beta-codex"}, {"answer", "uid:agt-beta-codex", "permission-0000000000000001", "--allow"}} {
		_, _, err := runRoute(t, fixture.command, append([]string{"approval"}, verb...)...)
		if err == nil || !strings.Contains(err.Error(), "(permission-provider-unsupported)") || !strings.Contains(err.Error(), "projmux agent approval review") ||
			!strings.Contains(err.Error(), "nothing was changed") {
			t.Fatalf("Codex %s err = %v, want a refusal pointing at review", verb[0], err)
		}
	}
	if _, err := os.Stat(filepath.Dir(fixture.approvals.Path())); !os.IsNotExist(err) {
		t.Fatalf("a Codex refusal touched the store: %v", err)
	}

	stdout, _, err := runRoute(t, fixture.command, "approval", "list", "uid:"+questionTestAgent)
	if err != nil || stdout != "agent/codex approvals answering projmux\nno waiting permission requests\n"+agentPermissionListNote {
		t.Fatalf("empty list = %q, %v", stdout, err)
	}
	_, _, err = runRoute(t, fixture.command, "approval", "answer", "uid:"+questionTestAgent, "permission-0000000000000009", "--allow")
	if err == nil || !strings.Contains(err.Error(), "(permission-not-found)") {
		t.Fatalf("unknown id err = %v", err)
	}

	fixture.answering = config.AgentApprovalAnsweringClaude
	_, _, err = runRoute(t, fixture.command, "approval", "answer", "uid:"+questionTestAgent, "permission-0000000000000009", "--allow")
	if err == nil || !strings.Contains(err.Error(), "(permission-answering-off)") {
		t.Fatalf("way 1 answer err = %v", err)
	}
}

// claudePermissionHookChildEnv makes the test binary run the hook entry in its
// own process: an unrecovered panic and a real SIGTERM are process-level.
const (
	claudePermissionHookChildEnv = "PMX_TEST_CLAUDE_PERMISSION_HOOK_CHILD"
	claudePermissionHookDirEnv   = "PMX_TEST_CLAUDE_PERMISSION_HOOK_DIR"
)

// exitIfClaudePermissionHookChild is the child side, run from TestMain.
func exitIfClaudePermissionHookChild() {
	mode := os.Getenv(claudePermissionHookChildEnv)
	if mode == "" {
		return
	}
	dir := os.Getenv(claudePermissionHookDirEnv)
	newHook := func() claudePermissionHook {
		hook := claudePermissionHook{
			loadRegistry: func() (coremetadata.Registry, error) {
				if strings.HasSuffix(mode, "registry-panic") {
					panic("registry bug")
				}
				data, err := os.ReadFile(filepath.Join(dir, "registry.json"))
				if err != nil {
					return coremetadata.Registry{}, err
				}
				var registry coremetadata.Registry
				err = json.Unmarshal(data, &registry)
				return registry, err
			},
			store: func() (*agentapproval.Store, error) {
				return agentapproval.NewStoreAt(filepath.Join(dir, "requests.json")), nil
			},
			answering: func() config.AgentApprovalAnswering { return config.AgentApprovalAnsweringProjmux },
			window:    func() time.Duration { return time.Minute },
			poll:      10 * time.Millisecond,
			newID:     agentapproval.NewID,
			now:       time.Now,
		}
		if strings.HasSuffix(mode, "wait-panic") {
			hook.readRecord = func(*agentapproval.Store, string) (agentapproval.Record, bool, error) { panic("wait bug") }
		}
		return hook
	}
	args := []string{"--pane=" + questionTestPane}
	if strings.HasPrefix(mode, "unwrapped-") {
		newHook().run(context.Background(), args, os.Stdin, os.Stdout, os.Stderr)
		os.Exit(0)
	}
	if err := runClaudePermissionHookWith(newHook, args, os.Stdin, os.Stdout, os.Stderr); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

// startClaudePermissionHookChild starts the child on a directory holding the
// fixture Registry.
func startClaudePermissionHookChild(t *testing.T, mode string) (*exec.Cmd, *bytes.Buffer, string) {
	t.Helper()
	fixture := newPermissionFixture(t, config.AgentApprovalAnsweringProjmux)
	dir := t.TempDir()
	data, err := json.Marshal(fixture.resources.registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "registry.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^$")
	command.Env = append(os.Environ(), claudePermissionHookChildEnv+"="+mode, claudePermissionHookDirEnv+"="+dir)
	command.Stdin = strings.NewReader(permissionTestPayload("default", "Bash", permissionTestInput, ""))
	var stdout bytes.Buffer
	command.Stdout = &stdout
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill() })
	return command, &stdout, dir
}

func waitClaudePermissionHookChild(t *testing.T, command *exec.Cmd) int {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		var exitErr *exec.ExitError
		switch {
		case err == nil:
			return 0
		case errors.As(err, &exitErr):
			return exitErr.ExitCode()
		default:
			t.Fatalf("wait child: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the child hook did not exit")
	}
	return -1
}

// Acceptance 4: a panic before or after the record exists exits 0 with no
// output and closes the record; the control shows Go's own exit 2.
func TestClaudePermissionHookPanicExitsZeroWithNoOutput(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		mode     string
		recorded bool
		wantExit int
	}{
		{mode: "registry-panic", wantExit: 0},
		{mode: "wait-panic", recorded: true, wantExit: 0},
		{mode: "unwrapped-registry-panic", wantExit: 2},
	} {
		t.Run(test.mode, func(t *testing.T) {
			t.Parallel()
			command, stdout, dir := startClaudePermissionHookChild(t, test.mode)
			if code := waitClaudePermissionHookChild(t, command); code != test.wantExit || stdout.Len() != 0 {
				t.Fatalf("child exit %d stdout %q, want exit %d and no output", code, stdout.String(), test.wantExit)
			}
			records, err := agentapproval.NewStoreAt(filepath.Join(dir, "requests.json")).List(questionTestAgent)
			if err != nil {
				t.Fatal(err)
			}
			if test.recorded != (len(records) == 1) || (test.recorded && records[0].State != agentapproval.StateClosed) {
				t.Fatalf("records = %+v, want recorded=%v and closed", records, test.recorded)
			}
		})
	}
}

// Acceptance 4: SIGTERM, which Claude Code sends for a terminal "No", SIGINT,
// and SIGHUP end the wait with exit 0, no output, and a closed record.
func TestClaudePermissionHookSignalsExitZeroWithNoOutput(t *testing.T) {
	t.Parallel()

	for _, signal := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP} {
		t.Run(signal.String(), func(t *testing.T) {
			t.Parallel()
			command, stdout, dir := startClaudePermissionHookChild(t, "wait")
			store := agentapproval.NewStoreAt(filepath.Join(dir, "requests.json"))
			deadline := time.Now().Add(20 * time.Second)
			for {
				if records, err := store.List(questionTestAgent); err == nil && len(records) == 1 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("the child never recorded its request")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err := command.Process.Signal(signal); err != nil {
				t.Fatal(err)
			}
			if code := waitClaudePermissionHookChild(t, command); code != 0 || stdout.Len() != 0 {
				t.Fatalf("child exit %d stdout %q, want exit 0 and no output", code, stdout.String())
			}
			records, err := store.List(questionTestAgent)
			if err != nil || len(records) != 1 || records[0].State != agentapproval.StateClosed {
				t.Fatalf("records = %+v (%v), want one closed", records, err)
			}
		})
	}
}

func TestClaudePermissionHookRouteSkipsAutomaticHookMigration(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"internal", claudePermissionHookRoute, "--pane=pan-1"},
		{"agent", "approval", "list", "uid:agt-1"},
		{"agent", "approval", "answer", "uid:agt-1", "permission-0000000000000001", "--allow"},
	} {
		if shouldRunLegacyHookMigrations(args) {
			t.Errorf("shouldRunLegacyHookMigrations(%q) = true", args)
		}
	}
	if !shouldRunLegacyHookMigrations([]string{"agent", "approval", "review", "uid:agt-1"}) {
		t.Error("agent approval review stopped running the hook migration it ran before")
	}
}
