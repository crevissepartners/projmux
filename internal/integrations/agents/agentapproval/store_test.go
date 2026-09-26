package agentapproval

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

var storeTestEpoch = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

// testClock is a settable store clock.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newTestStore(t *testing.T) (*Store, *testClock) {
	t.Helper()
	clock := &testClock{now: storeTestEpoch}
	return NewStore(t.TempDir()).WithClock(clock.Now), clock
}

func testID(n int) string { return fmt.Sprintf("permission-%016x", n) }

const testBashInput = `{"command": "rm -rf build", "description": "clean"}`

func createTestRecord(t *testing.T, store *Store, clock *testClock, n int, agentUID, session, tool, input string) Record {
	t.Helper()
	record, err := store.Create(Record{
		ID: testID(n), AgentUID: agentUID, PaneUID: "pan-" + agentUID, SessionID: session,
		ToolName: tool, ToolInput: json.RawMessage(input), Deadline: clock.Now().Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	return record
}

// readAudit decodes every audit line of store, oldest first.
func readAudit(t *testing.T, path string) []AuditLine {
	t.Helper()
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var lines []AuditLine
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		decoder := json.NewDecoder(strings.NewReader(scanner.Text()))
		decoder.DisallowUnknownFields()
		var line AuditLine
		if err := decoder.Decode(&line); err != nil {
			t.Fatalf("audit line %q: %v", scanner.Text(), err)
		}
		lines = append(lines, line)
	}
	return lines
}

func auditEvents(lines []AuditLine) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, line.Event)
	}
	return out
}

func TestStoreTransitionsAndRefusals(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		settle    func(*Store, *testClock, Record) (Record, error)
		wantState State
		wantErr   error
	}{
		{name: "allow", settle: func(s *Store, _ *testClock, r Record) (Record, error) {
			return s.Answer(r.ID, r.AgentUID, true, ViaCLI)
		}, wantState: StateAllowed, wantErr: ErrNotPending},
		{name: "deny", settle: func(s *Store, _ *testClock, r Record) (Record, error) {
			return s.Answer(r.ID, r.AgentUID, false, ViaWeb)
		}, wantState: StateDenied, wantErr: ErrNotPending},
		{name: "close", settle: func(s *Store, _ *testClock, r Record) (Record, error) { return s.Close(r.ID, CloseReasonCanceled) }, wantState: StateClosed, wantErr: ErrClosed},
		{name: "settle at the deadline", settle: func(s *Store, c *testClock, r Record) (Record, error) {
			c.Advance(5 * time.Minute)
			return s.Settle(r.ID)
		}, wantState: StateExpired, wantErr: ErrExpired},
		{name: "close past the deadline expires", settle: func(s *Store, c *testClock, r Record) (Record, error) {
			c.Advance(6 * time.Minute)
			return s.Close(r.ID, CloseReasonCanceled)
		}, wantState: StateExpired, wantErr: ErrExpired},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store, clock := newTestStore(t)
			record := createTestRecord(t, store, clock, 1, "agt-a", "sess-1", "Bash", testBashInput)
			if early, err := store.Settle(record.ID); err != nil || early.State != StateWaiting {
				t.Fatalf("early Settle = %s, %v; want waiting", early.State, err)
			}
			settled, err := test.settle(store, clock, record)
			if err != nil || settled.State != test.wantState {
				t.Fatalf("settle = %s, %v; want %s", settled.State, err, test.wantState)
			}
			for _, allow := range []bool{true, false} {
				if _, err := store.Answer(record.ID, "agt-a", allow, ViaCLI); !errors.Is(err, test.wantErr) {
					t.Fatalf("late answer allow=%t err = %v, want %v", allow, err, test.wantErr)
				}
			}
			if got, _, _ := store.Get(record.ID); got.State != test.wantState {
				t.Fatalf("a refused answer changed the record to %s", got.State)
			}
			// A later close or settle keeps the first outcome.
			if again, err := store.Close(record.ID, CloseReasonCanceled); err != nil || again.State != test.wantState {
				t.Fatalf("Close after %s = %s, %v", test.wantState, again.State, err)
			}
		})
	}
}

func TestStoreAnswerRefusesAnotherAgentAndUnknownIDs(t *testing.T) {
	t.Parallel()

	store, clock := newTestStore(t)
	record := createTestRecord(t, store, clock, 1, "agt-a", "sess-1", "Bash", testBashInput)
	if _, err := store.Answer(record.ID, "agt-b", true, ViaCLI); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other Agent err = %v, want ErrNotFound", err)
	}
	if _, err := store.Answer(testID(9), "agt-a", true, ViaCLI); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id err = %v, want ErrNotFound", err)
	}
	if got, _, _ := store.Get(record.ID); got.State != StateWaiting {
		t.Fatalf("state = %s, want waiting", got.State)
	}
	lines := readAudit(t, store.AuditPath())
	if got := strings.Join(auditEvents(lines), ","); got != "requested,refused,refused" {
		t.Fatalf("audit events = %s", got)
	}
	if lines[1].Reason != "not-found" || lines[2].RequestID != testID(9) {
		t.Fatalf("refusal lines = %+v", lines[1:])
	}
}

// TestStoreFirstAnswerWins races two answers from separate goroutines: exactly
// one lands, and the other is refused as not pending.
func TestStoreFirstAnswerWins(t *testing.T) {
	t.Parallel()

	for round := range 8 {
		store, clock := newTestStore(t)
		record := createTestRecord(t, store, clock, round+1, "agt-a", "sess-1", "Bash", testBashInput)
		var wg sync.WaitGroup
		results := make([]error, 2)
		for i := range 2 {
			wg.Go(func() {
				_, results[i] = store.Answer(record.ID, "agt-a", i == 0, ViaCLI)
			})
		}
		wg.Wait()
		won, refused := 0, 0
		for _, err := range results {
			switch {
			case err == nil:
				won++
			case errors.Is(err, ErrNotPending):
				refused++
			default:
				t.Fatalf("answer err = %v", err)
			}
		}
		if won != 1 || refused != 1 {
			t.Fatalf("round %d: won %d refused %d, want one each", round, won, refused)
		}
	}
}

func TestStoreCreateRefusesInvalidRecords(t *testing.T) {
	t.Parallel()

	store, clock := newTestStore(t)
	valid := Record{ID: testID(1), AgentUID: "agt-a", ToolName: "Bash", ToolInput: json.RawMessage(testBashInput), Deadline: clock.Now().Add(time.Minute)}
	for name, mutate := range map[string]func(*Record){
		"bad id":          func(r *Record) { r.ID = "question-0000000000000001" },
		"no agent":        func(r *Record) { r.AgentUID = "" },
		"no tool":         func(r *Record) { r.ToolName = " " },
		"array input":     func(r *Record) { r.ToolInput = json.RawMessage(`[1]`) },
		"malformed input": func(r *Record) { r.ToolInput = json.RawMessage(`{"a":`) },
		"oversized input": func(r *Record) {
			r.ToolInput = json.RawMessage(`{"a":"` + strings.Repeat("x", MaxToolInputBytes) + `"}`)
		},
		"deadline not set": func(r *Record) { r.Deadline = time.Time{} },
		"deadline passed":  func(r *Record) { r.Deadline = clock.Now().Add(-time.Second) },
	} {
		record := valid
		mutate(&record)
		if _, err := store.Create(record); !errors.Is(err, ErrInvalidRecord) {
			t.Errorf("%s: err = %v, want ErrInvalidRecord", name, err)
		}
	}
	if _, err := os.Stat(store.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused create wrote the store: %v", err)
	}
	if _, err := store.Create(valid); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(valid); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("duplicate id err = %v", err)
	}
}

func TestStoreFilesArePrivateAndMalformedStoreRefuses(t *testing.T) {
	t.Parallel()

	store, clock := newTestStore(t)
	createTestRecord(t, store, clock, 1, "agt-a", "sess-1", "Bash", testBashInput)
	for _, path := range []string{store.Path(), store.AuditPath()} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v (%v), want 0600", path, info.Mode().Perm(), err)
		}
	}
	if info, err := os.Stat(filepath.Dir(store.Path())); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("store directory mode = %v (%v), want 0700", info.Mode().Perm(), err)
	}
	if err := os.WriteFile(store.Path(), []byte(`{"version":1,"records":[{"id":"x"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Get(testID(1)); !errors.Is(err, ErrMalformedStore) {
		t.Fatalf("Get err = %v, want ErrMalformedStore", err)
	}
	if _, err := store.Answer(testID(1), "agt-a", true, ViaCLI); !errors.Is(err, ErrMalformedStore) {
		t.Fatalf("Answer err = %v, want ErrMalformedStore", err)
	}
}

// TestStoreAuditLinesCarryEveryFieldAndOnlyASummary holds the audit contract:
// one line per event with the request, Agent, Pane, session, subagent type,
// tool, bounded input summary, UTC times, and via, and never the full input.
func TestStoreAuditLinesCarryEveryFieldAndOnlyASummary(t *testing.T) {
	t.Parallel()

	store, clock := newTestStore(t)
	secret := `{"command":"deploy --token=abc","file_path":"/x","extra":"` + strings.Repeat("s", 500) + `"}`
	record, err := store.Create(Record{
		ID: testID(1), AgentUID: "agt-a", PaneUID: "pan-a", SessionID: "sess-1", AgentType: "Explore",
		ToolName: "mcp__db__query", ToolInput: json.RawMessage(secret), Deadline: clock.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(3 * time.Second)
	if _, err := store.Answer(record.ID, "agt-a", true, ViaPopup); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Second)
	if _, err := store.Answer(record.ID, "agt-a", false, ViaCLI); !errors.Is(err, ErrNotPending) {
		t.Fatal(err)
	}
	second := createTestRecord(t, store, clock, 2, "agt-a", "sess-1", "Bash", testBashInput)
	if _, err := store.Answer(second.ID, "agt-a", false, ViaCLI); err != nil {
		t.Fatal(err)
	}
	third := createTestRecord(t, store, clock, 3, "agt-a", "sess-1", "Bash", testBashInput)
	clock.Advance(5 * time.Minute)
	if _, err := store.Settle(third.ID); err != nil {
		t.Fatal(err)
	}
	fourth := createTestRecord(t, store, clock, 4, "agt-a", "sess-1", "Bash", testBashInput)
	if _, err := store.Close(fourth.ID, CloseReasonFailed); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(store.AuditPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), strings.Repeat("s", 50)) || strings.Contains(string(raw), "deploy") {
		t.Fatalf("audit log carries tool input values: %s", raw)
	}
	lines := readAudit(t, store.AuditPath())
	if got := strings.Join(auditEvents(lines), ","); got != "requested,allowed,refused,requested,denied,requested,expired,requested,closed" {
		t.Fatalf("audit events = %s", got)
	}
	requested, allowed := lines[0], lines[1]
	if requested.RequestID != record.ID || requested.AgentUID != "agt-a" || requested.PaneUID != "pan-a" || requested.SessionID != "sess-1" ||
		requested.AgentType != "Explore" || requested.ToolName != "mcp__db__query" || requested.Input != "keys: command,extra,file_path" ||
		!requested.RequestedAt.Equal(storeTestEpoch) || !requested.DecidedAt.IsZero() || requested.At.Location() != time.UTC {
		t.Fatalf("requested line = %+v", requested)
	}
	if allowed.Via != ViaPopup || !allowed.DecidedAt.Equal(storeTestEpoch.Add(3*time.Second)) || !allowed.RequestedAt.Equal(storeTestEpoch) {
		t.Fatalf("allowed line = %+v", allowed)
	}
	if lines[2].Via != ViaCLI || lines[2].Reason != string(StateAllowed) {
		t.Fatalf("refused line = %+v", lines[2])
	}
	if lines[4].Input != "rm -rf build" || lines[4].Via != ViaCLI {
		t.Fatalf("denied line = %+v", lines[4])
	}
	if !lines[6].DecidedAt.Equal(third.Deadline) {
		t.Fatalf("expired line = %+v", lines[6])
	}
	if lines[8].Reason != CloseReasonFailed {
		t.Fatalf("closed line = %+v", lines[8])
	}
}

// TestStoreAuditRotatesAtItsCap holds the size cap: the active log rotates to
// one retained generation before it would pass the limit, and both stay
// private.
func TestStoreAuditRotatesAtItsCap(t *testing.T) {
	t.Parallel()

	store, clock := newTestStore(t)
	store = store.WithAuditLimit(900)
	for n := range 12 {
		createTestRecord(t, store, clock, n+1, "agt-a", "sess-1", "Bash", testBashInput)
	}
	for _, path := range []string{store.AuditPath(), store.AuditPath() + auditRotatedSuffix} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v (%v), want a private file", path, info, err)
		}
		if info.Size() > 900 {
			t.Fatalf("%s holds %d bytes, over the 900 byte cap", path, info.Size())
		}
	}
	matches, _ := filepath.Glob(store.AuditPath() + "*")
	if len(matches) != 2 {
		t.Fatalf("audit generations = %v, want the active log and one rotated", matches)
	}
	if last := readAudit(t, store.AuditPath()); len(last) == 0 || last[len(last)-1].RequestID != testID(12) {
		t.Fatalf("the active log does not end with the newest line: %+v", last)
	}
}

func TestInputSummaryIsBoundedAndOneLine(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("é", 400)
	for _, test := range []struct {
		tool, input, want string
	}{
		{tool: "Bash", input: `{"command":"ls -la\nrm x\ttab","description":"d"}`, want: "ls -la rm x tab"},
		{tool: "Write", input: `{"file_path":"/srv/a.go","content":"secret"}`, want: "/srv/a.go"},
		{tool: "Edit", input: `{"file_path":"/srv/b.go","old_string":"a","new_string":"b"}`, want: "/srv/b.go"},
		{tool: "NotebookEdit", input: `{"notebook_path":"/n.ipynb","new_source":"x"}`, want: "/n.ipynb"},
		{tool: "mcp__x__y", input: `{"b":1,"a":{"secret":true}}`, want: "keys: a,b"},
		{tool: "Bash", input: `{"description":"no command"}`, want: "keys: description"},
		{tool: "Bash", input: `[1,2]`, want: ""},
	} {
		if got := InputSummary(test.tool, json.RawMessage(test.input)); got != test.want {
			t.Errorf("InputSummary(%s, %s) = %q, want %q", test.tool, test.input, got, test.want)
		}
	}
	got := InputSummary("Bash", json.RawMessage(`{"command":"`+long+`"}`))
	if utf8.RuneCountInString(got) != MaxInputSummaryRunes || !strings.HasSuffix(got, "…") || strings.ContainsAny(got, "\n\r") {
		t.Fatalf("long summary = %d runes %q", utf8.RuneCountInString(got), got)
	}
}

func TestCanonicalInputComparesJSONValues(t *testing.T) {
	t.Parallel()

	a, okA := CanonicalInput(json.RawMessage(`{"b": 1.50, "a": {"y": [1, 2], "x": "<&>"}}`))
	b, okB := CanonicalInput(json.RawMessage(`{"a":{"x":"<&>","y":[1,2]},"b":1.50}`))
	if !okA || !okB || a != b {
		t.Fatalf("canonical forms differ: %q vs %q", a, b)
	}
	if c, _ := CanonicalInput(json.RawMessage(`{"a":{"x":"<&>","y":[2,1]},"b":1.50}`)); c == a {
		t.Fatal("a different value compared equal")
	}
	for _, bad := range []string{``, `{`, `{} {}`} {
		if _, ok := CanonicalInput(json.RawMessage(bad)); ok {
			t.Errorf("CanonicalInput(%q) accepted", bad)
		}
	}
}

// TestCloseAnsweredInTerminalClosesExactlyOneMatch holds the terminal-answer
// close: exactly one waiting match of session, tool, and canonical input is
// closed and audited; zero or several matches close nothing.
func TestCloseAnsweredInTerminalClosesExactlyOneMatch(t *testing.T) {
	t.Parallel()

	store, clock := newTestStore(t)
	record := createTestRecord(t, store, clock, 1, "agt-a", "sess-1", "Bash", testBashInput)
	createTestRecord(t, store, clock, 2, "agt-a", "sess-2", "Bash", testBashInput)
	createTestRecord(t, store, clock, 3, "agt-a", "sess-1", "Write", testBashInput)
	createTestRecord(t, store, clock, 4, "agt-a", "sess-1", "Bash", `{"command":"rm -rf dist"}`)

	for name, test := range map[string]struct{ session, tool, input string }{
		"other input":   {session: "sess-1", tool: "Bash", input: `{"command":"rm -rf build"}`},
		"other tool":    {session: "sess-1", tool: "Read", input: testBashInput},
		"other session": {session: "sess-9", tool: "Bash", input: testBashInput},
		"no session":    {session: "", tool: "Bash", input: testBashInput},
	} {
		if closed, err := store.CloseAnsweredInTerminal(test.session, test.tool, json.RawMessage(test.input)); closed || err != nil {
			t.Fatalf("%s: closed=%t err=%v, want nothing closed", name, closed, err)
		}
	}
	closed, err := store.CloseAnsweredInTerminal("sess-1", "Bash", json.RawMessage(`{"description":"clean","command":"rm -rf build"}`))
	if err != nil || !closed {
		t.Fatalf("exact match closed=%t err=%v", closed, err)
	}
	if got, _, _ := store.Get(record.ID); got.State != StateClosed {
		t.Fatalf("state = %s, want closed", got.State)
	}
	if _, err := store.Answer(record.ID, "agt-a", true, ViaCLI); !errors.Is(err, ErrClosed) {
		t.Fatalf("answer after the terminal answer err = %v, want ErrClosed", err)
	}
	lines := readAudit(t, store.AuditPath())
	var closedLine AuditLine
	for _, line := range lines {
		if line.Event == AuditClosed {
			closedLine = line
		}
	}
	if closedLine.RequestID != record.ID || closedLine.Reason != CloseReasonAnsweredInTerminal {
		t.Fatalf("closed audit line = %+v", closedLine)
	}
	for n := 2; n <= 4; n++ {
		if got, _, _ := store.Get(testID(n)); got.State != StateWaiting {
			t.Fatalf("record %d = %s, want waiting", n, got.State)
		}
	}

	// Two identical waiting requests are ambiguous: neither is closed.
	createTestRecord(t, store, clock, 5, "agt-a", "sess-3", "Bash", testBashInput)
	createTestRecord(t, store, clock, 6, "agt-a", "sess-3", "Bash", testBashInput)
	if closed, err := store.CloseAnsweredInTerminal("sess-3", "Bash", json.RawMessage(testBashInput)); closed || err != nil {
		t.Fatalf("two matches closed=%t err=%v, want nothing closed", closed, err)
	}
	for _, n := range []int{5, 6} {
		if got, _, _ := store.Get(testID(n)); got.State != StateWaiting {
			t.Fatalf("record %d = %s, want waiting", n, got.State)
		}
	}
}

// TestCloseAnsweredInTerminalWithNoStoreTouchesNothing holds the cheap path:
// with no store file it creates no directory, lock, or audit log.
func TestCloseAnsweredInTerminalWithNoStoreTouchesNothing(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)
	if closed, err := store.CloseAnsweredInTerminal("sess-1", "Bash", json.RawMessage(testBashInput)); closed || err != nil {
		t.Fatalf("closed=%t err=%v", closed, err)
	}
	if _, err := os.Stat(filepath.Dir(store.Path())); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("store directory stat err = %v, want not exist", err)
	}
}

// TestStorePruneKeepsWaitingRecords holds the bound: terminal records make
// room, a store full of waiting records refuses a new one.
func TestStorePruneKeepsWaitingRecords(t *testing.T) {
	t.Parallel()

	store, clock := newTestStore(t)
	for n := range maxRecords {
		createTestRecord(t, store, clock, n+1, "agt-a", "sess-1", "Bash", testBashInput)
	}
	extra := Record{ID: testID(999), AgentUID: "agt-a", ToolName: "Bash", ToolInput: json.RawMessage(testBashInput), Deadline: clock.Now().Add(time.Minute)}
	if _, err := store.Create(extra); !errors.Is(err, ErrCapacity) {
		t.Fatalf("full store err = %v, want ErrCapacity", err)
	}
	if _, err := store.Close(testID(1), CloseReasonCanceled); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(extra); err != nil {
		t.Fatalf("create after a terminal record err = %v", err)
	}
	if _, found, _ := store.Get(testID(1)); found {
		t.Fatal("the oldest terminal record was not pruned")
	}
}
