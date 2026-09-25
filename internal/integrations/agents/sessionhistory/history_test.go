package sessionhistory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

var t0 = time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)

func claudeRef(sessionID, transcript string, at time.Time) *coremetadata.AgentSessionRef {
	return &coremetadata.AgentSessionRef{
		Provider: ProviderClaude, ObservedAt: at,
		Claude: &coremetadata.ClaudeSessionRef{SessionID: sessionID, TranscriptPath: transcript},
	}
}

func observed(t *testing.T, agentUID, sessionID string, at time.Time) Record {
	t.Helper()
	record, ok := RecordFor(agentUID, claudeRef(sessionID, "/t/"+sessionID+".jsonl", at), SourceObserved)
	if !ok {
		t.Fatalf("RecordFor(%s, %s) refused a Claude ref", agentUID, sessionID)
	}
	return record
}

func agentWith(uid string, ref *coremetadata.AgentSessionRef) coremetadata.Agent {
	var agent coremetadata.Agent
	agent.Metadata.UID = uid
	agent.Spec.Provider = ProviderClaude
	agent.Status.SessionRef = ref
	return agent
}

func TestRecordForAcceptsOnlyClaudeRefsWithASessionID(t *testing.T) {
	cases := map[string]*coremetadata.AgentSessionRef{
		"nil":        nil,
		"codex":      {Provider: "codex", ObservedAt: t0, Codex: &coremetadata.CodexSessionRef{ThreadID: "thread-1"}},
		"no session": claudeRef(" ", "/t/x.jsonl", t0),
	}
	for name, ref := range cases {
		if _, ok := RecordFor("agent-1", ref, SourceObserved); ok {
			t.Errorf("%s: RecordFor accepted it", name)
		}
	}
	if _, ok := RecordFor("", claudeRef("s", "", t0), SourceObserved); ok {
		t.Error("RecordFor accepted a blank agent uid")
	}
}

func TestAppendThenListReturnsHistoryAndMarksTheCurrentRef(t *testing.T) {
	dir := t.TempDir()
	for _, record := range []Record{observed(t, "agent-1", "A", t0), observed(t, "agent-1", "B", t0.Add(time.Minute)), observed(t, "agent-2", "Z", t0)} {
		if err := Append(dir, record); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	info, err := os.Stat(Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("history file mode = %o, want 600", perm)
	}

	result, err := List(dir, agentWith("agent-1", claudeRef("B", "/t/B.jsonl", t0.Add(time.Minute))))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := sessionSources(result.Sessions)
	if want := []string{"A/observed", "B/current"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sessions = %v, want %v", got, want)
	}
	if result.CorruptLines != 0 {
		t.Fatalf("corrupt = %d, want 0", result.CorruptLines)
	}
}

func TestListWithoutAHistoryFileReturnsTheRegistryRefAsCurrent(t *testing.T) {
	dir := t.TempDir()
	result, err := List(dir, agentWith("agent-1", claudeRef("only", "/t/only.jsonl", t0)))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(result.Sessions) != 1 {
		t.Fatalf("sessions = %#v, want one current row", result.Sessions)
	}
	row := result.Sessions[0]
	if row.Source != SourceCurrent || row.SessionID != "only" || row.TranscriptPath != "/t/only.jsonl" || !row.ObservedAt.Equal(t0) || row.Provider != ProviderClaude || row.AgentUID != "agent-1" {
		t.Fatalf("row = %#v", row)
	}
	if _, err := os.Stat(Path(dir)); !os.IsNotExist(err) {
		t.Fatalf("List created the history file: %v", err)
	}
}

func TestListOfAnAgentWithNothingIsAnEmptyNonNilList(t *testing.T) {
	result, err := List(t.TempDir(), agentWith("agent-1", nil))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(result)
	if !strings.Contains(string(body), `"sessions":[]`) {
		t.Fatalf("empty list encodes as %s, want an empty array", body)
	}
}

func TestMergeRuleForAConversationSeenMoreThanOnce(t *testing.T) {
	// A, then B, then back to A: A appears once, at its latest observation,
	// and is the current row; B keeps its own row before it.
	history := []Record{
		observed(t, "agent-1", "A", t0),
		observed(t, "agent-1", "B", t0.Add(time.Minute)),
	}
	current, _ := RecordFor("agent-1", claudeRef("A", "/t/A-new.jsonl", t0.Add(2*time.Minute)), SourceCurrent)
	rows := Merge(history, &current)
	if got, want := sessionSources(rows), []string{"B/observed", "A/current"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	if rows[1].TranscriptPath != "/t/A-new.jsonl" || !rows[1].ObservedAt.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("A row = %#v, want the latest observation's path and time", rows[1])
	}

	// A current ref older than a duplicate observation keeps the later time
	// but still marks the row current.
	stale, _ := RecordFor("agent-1", claudeRef("B", "", t0), SourceCurrent)
	rows = Merge(history, &stale)
	if got, want := sessionSources(rows), []string{"A/observed", "B/current"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	if rows[1].TranscriptPath != "/t/B.jsonl" || !rows[1].ObservedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("B row = %#v, want the history row's path and time", rows[1])
	}

	// Equal instants keep input order.
	tied := []Record{observed(t, "agent-1", "X", t0), observed(t, "agent-1", "Y", t0)}
	if got, want := sessionSources(Merge(tied, nil)), []string{"X/observed", "Y/observed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tied rows = %v, want %v", got, want)
	}
}

func TestReadSkipsAndCountsCorruptLines(t *testing.T) {
	dir := t.TempDir()
	if err := Append(dir, observed(t, "agent-1", "A", t0)); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(Path(dir), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	// A torn tail, a non-object, and a row with no session id.
	if _, err := file.WriteString(`{"agentUID":"agent-1","sessionId":"torn` + "\nnot json\n" + `{"agentUID":"agent-1","provider":"claude"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	file.Close()
	// The next append seals the torn tail with its leading delimiter.
	if err := Append(dir, observed(t, "agent-1", "B", t0.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}

	read, err := Read(dir, "agent-1")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if read.Corrupt != 3 {
		t.Fatalf("corrupt = %d, want 3", read.Corrupt)
	}
	if got, want := sessionSources(read.Records), []string{"A/observed", "B/observed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("records = %v, want %v", got, want)
	}
	result, err := List(dir, agentWith("agent-1", nil))
	if err != nil || result.CorruptLines != 3 {
		t.Fatalf("List corrupt = %d, err = %v; want 3", result.CorruptLines, err)
	}
}

func TestConcurrentAppendsLeaveEveryLineIntact(t *testing.T) {
	dir := t.TempDir()
	const writers, each = 16, 25
	long := strings.Repeat("p", 2048)
	var wg sync.WaitGroup
	errs := make(chan error, writers*each)
	for w := range writers {
		wg.Go(func() {
			for i := range each {
				record := Record{
					AgentUID: fmt.Sprintf("agent-%d", w), Provider: ProviderClaude,
					SessionID: fmt.Sprintf("s-%d-%d", w, i), TranscriptPath: "/t/" + long,
					ObservedAt: t0.Add(time.Duration(i) * time.Second), Source: SourceObserved,
				}
				if err := Append(dir, record); err != nil {
					errs <- err
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("Append: %v", err)
	}
	read, err := Read(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if read.Corrupt != 0 || len(read.Records) != writers*each {
		t.Fatalf("read %d records and %d corrupt lines, want %d and 0", len(read.Records), read.Corrupt, writers*each)
	}
	seen := map[string]bool{}
	for _, record := range read.Records {
		if seen[record.SessionID] {
			t.Fatalf("session %s appears twice", record.SessionID)
		}
		seen[record.SessionID] = true
	}
}

func TestAppendFailsWhenTheStateDirIsAFile(t *testing.T) {
	parent := t.TempDir()
	blocker := filepath.Join(parent, "state")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Append(blocker, observed(t, "agent-1", "A", t0)); err == nil {
		t.Fatal("Append into a file succeeded")
	}
	if err := Append(parent, Record{AgentUID: "agent-1", Provider: "codex", SessionID: "x"}); err == nil {
		t.Fatal("Append accepted a non-Claude record")
	}
}

func TestRecordJSONKeysAreFixed(t *testing.T) {
	body, err := json.Marshal(observed(t, "agent-1", "A", t0))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for key := range fields {
		keys = append(keys, key)
	}
	want := []string{"agentUID", "observedAt", "provider", "sessionId", "source", "transcriptPath"}
	if !sameSet(keys, want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	if fields["observedAt"] != "2026-09-25T09:00:00Z" || fields["source"] != "observed" {
		t.Fatalf("record = %s", body)
	}
	if SourceEstimated != "estimated" {
		t.Fatal("the reserved estimated source changed spelling")
	}
}

func sessionSources(records []Record) []string {
	out := make([]string, 0, len(records))
	for _, record := range records {
		out = append(out, record.SessionID+"/"+string(record.Source))
	}
	return out
}

func sameSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	set := map[string]bool{}
	for _, key := range got {
		set[key] = true
	}
	for _, key := range want {
		if !set[key] {
			return false
		}
	}
	return true
}
