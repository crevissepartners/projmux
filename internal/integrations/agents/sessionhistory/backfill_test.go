package sessionhistory

import (
	"crypto/sha256"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// frameText is the text Claude shows for a delivered frame: a sentence of its
// own, then the frame object.
func frameText(targetUID string) string {
	type route struct {
		AgentUID string `json:"agentUID"`
		Provider string `json:"provider"`
	}
	// The field order of the rendered frame: kind first.
	body, _ := json.Marshal(struct {
		Kind          string `json:"kind"`
		SchemaVersion int    `json:"schemaVersion"`
		Authority     string `json:"authority"`
		MessageRef    string `json:"messageRef"`
		Source        route  `json:"source"`
		Target        route  `json:"target"`
		Payload       string `json:"payload"`
	}{
		Kind: "projmux-coordination", SchemaVersion: 2, Authority: "untrusted-coordination-only", MessageRef: "msg-1",
		Source: route{"sender", "claude"}, Target: route{targetUID, "claude"},
		Payload: `hello {"kind":"projmux-coordination"`,
	})
	return "Another Claude session sent a message:\n" + string(body)
}

func userLine(ts string, content any) map[string]any {
	return map[string]any{"type": "user", "timestamp": ts, "message": map[string]any{"role": "user", "content": content}}
}

func textBlocks(texts ...string) []map[string]any {
	blocks := make([]map[string]any, 0, len(texts))
	for _, text := range texts {
		blocks = append(blocks, map[string]any{"type": "text", "text": text})
	}
	return blocks
}

// writeTranscript writes <projects>/<project>/<session>.jsonl from records;
// a string record is written verbatim as one line.
func writeTranscript(t *testing.T, projects, project, session string, records ...any) string {
	t.Helper()
	dir := filepath.Join(projects, project)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, record := range records {
		if raw, ok := record.(string); ok {
			b.WriteString(raw + "\n")
			continue
		}
		body, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(body)
		b.WriteByte('\n')
	}
	path := filepath.Join(dir, session+".jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const (
	ts1 = "2026-08-01T10:00:00.123Z"
	ts2 = "2026-08-01T10:05:00Z"
	ts3 = "2026-08-01T11:30:00+09:00"
)

func runBackfill(t *testing.T, projects, state string, dryRun bool, current ...Record) BackfillReport {
	t.Helper()
	report, err := Backfill(BackfillOptions{ProjectsDir: projects, StateDir: state, DryRun: dryRun, Current: current})
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	sum := report.Attributed + report.AlreadyEstimated + report.SkippedObserved + report.Ambiguous + report.NoFrame + report.Unreadable
	if sum != report.Scanned {
		t.Fatalf("outcomes add up to %d, scanned %d: %#v", sum, report.Scanned, report)
	}
	return report
}

func counts(r BackfillReport) map[string]int {
	return map[string]int{
		"scanned": r.Scanned, "attributed": r.Attributed, "alreadyEstimated": r.AlreadyEstimated,
		"skippedObserved": r.SkippedObserved, "ambiguous": r.Ambiguous, "noFrame": r.NoFrame,
		"unreadable": r.Unreadable, "malformedLines": r.MalformedLines, "malformedFrames": r.MalformedFrames,
	}
}

func wantCounts(t *testing.T, r BackfillReport, want map[string]int) {
	t.Helper()
	got := counts(r)
	for key := range got {
		if got[key] != want[key] {
			t.Fatalf("counts = %v, want %v", got, want)
		}
	}
}

func TestBackfillSingleTargetTranscriptBecomesOneEstimatedRow(t *testing.T) {
	projects, state := t.TempDir(), t.TempDir()
	path := writeTranscript(t, projects, "-src-app", "sess-1",
		map[string]any{"type": "queue-operation", "timestamp": ts1},
		userLine(ts2, frameText("agent-a")),
		userLine(ts2, textBlocks("prefix", frameText("agent-a"))),
		map[string]any{"type": "assistant", "timestamp": ts3, "message": map[string]any{"content": "ok"}},
	)
	report := runBackfill(t, projects, state, false)
	wantCounts(t, report, map[string]int{"scanned": 1, "attributed": 1})
	if len(report.Rows) != 1 {
		t.Fatalf("rows = %#v", report.Rows)
	}
	row := report.Rows[0]
	first, _ := time.Parse(time.RFC3339Nano, ts1)
	last, _ := time.Parse(time.RFC3339Nano, ts3)
	if row.AgentUID != "agent-a" || row.Provider != ProviderClaude || row.SessionID != "sess-1" || row.TranscriptPath != path ||
		row.Source != SourceEstimated || !row.ObservedAt.Equal(first) || row.ObservedAt.Location() != time.UTC ||
		row.LastRecordAt == nil || !row.LastRecordAt.Equal(last) || row.LastRecordAt.Location() != time.UTC {
		t.Fatalf("row = %#v", row)
	}
	read, err := Read(state, "agent-a")
	if err != nil || read.Corrupt != 0 || len(read.Records) != 1 || !reflect.DeepEqual(read.Records[0], row) {
		t.Fatalf("history = %#v (err %v), want the reported row", read, err)
	}
	if report.HistoryPath != Path(state) || report.ProjectsDir != projects || report.DryRun {
		t.Fatalf("report = %#v", report)
	}
}

func TestBackfillOnlySingleTargetTranscriptsBecomeEstimated(t *testing.T) {
	projects, state := t.TempDir(), t.TempDir()
	writeTranscript(t, projects, "p", "two-targets",
		userLine(ts1, frameText("agent-a")), userLine(ts2, textBlocks(frameText("agent-b"))))
	writeTranscript(t, projects, "p", "no-frame", userLine(ts1, "just a prompt"))
	writeTranscript(t, projects, "p", "same-target-twice",
		userLine(ts1, frameText("agent-a")), userLine(ts2, frameText("agent-a")+"\n"+frameText("agent-a")))
	writeTranscript(t, projects, "q", "no-timestamp", map[string]any{"type": "user", "message": map[string]any{"content": frameText("agent-c")}})
	report := runBackfill(t, projects, state, false)
	wantCounts(t, report, map[string]int{"scanned": 4, "attributed": 1, "ambiguous": 1, "noFrame": 1, "unreadable": 1})
	if len(report.Rows) != 1 || report.Rows[0].SessionID != "same-target-twice" || report.Rows[0].AgentUID != "agent-a" {
		t.Fatalf("rows = %#v", report.Rows)
	}
	if read, _ := Read(state, ""); len(read.Records) != 1 {
		t.Fatalf("history = %#v, want the one single-target row", read)
	}
}

func TestBackfillReadsOnlyDeliveredUserTextFrames(t *testing.T) {
	projects, state := t.TempDir(), t.TempDir()
	quoted := frameText("agent-x")
	writeTranscript(t, projects, "p", "quoted-elsewhere",
		userLine(ts1, "hi"),
		// An assistant record, a tool_result block, a sidechain user record,
		// an attachment, and a queue operation all quote a frame.
		map[string]any{"type": "assistant", "timestamp": ts1, "message": map[string]any{"content": textBlocks(quoted)}},
		userLine(ts1, []map[string]any{{"type": "tool_result", "content": quoted}}),
		map[string]any{"type": "user", "isSidechain": true, "timestamp": ts1, "message": map[string]any{"content": quoted}},
		map[string]any{"type": "attachment", "timestamp": ts1, "content": quoted},
		map[string]any{"type": "queue-operation", "timestamp": ts1, "content": quoted},
	)
	writeTranscript(t, projects, "p", "malformed-then-frame",
		"not json {",
		"[1,2]",
		// A quoted fragment of a frame inside a wrapper does not decode; the
		// real frame after it does.
		userLine(ts1, `<agent-message from="x">{"kind":"projmux-coordination","target":{"agentUID":"agent-y"</agent-message> then `+frameText("agent-z")),
	)
	report := runBackfill(t, projects, state, false)
	wantCounts(t, report, map[string]int{"scanned": 2, "attributed": 1, "noFrame": 1, "malformedLines": 2, "malformedFrames": 1})
	if len(report.Rows) != 1 || report.Rows[0].AgentUID != "agent-z" || report.Rows[0].SessionID != "malformed-then-frame" {
		t.Fatalf("rows = %#v", report.Rows)
	}
}

func TestBackfillScansOnlyTopLevelJSONLTranscripts(t *testing.T) {
	projects, state := t.TempDir(), t.TempDir()
	top := writeTranscript(t, projects, "p", "top", userLine(ts1, frameText("agent-a")))
	writeTranscript(t, filepath.Join(projects, "p"), "top", "subagents", userLine(ts1, frameText("agent-b")))
	writeTranscript(t, filepath.Join(projects, "p", "top"), "subagents", "agent-x", userLine(ts1, frameText("agent-b")))
	if err := os.WriteFile(filepath.Join(projects, "p", "notes.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projects, "loose.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(top, filepath.Join(projects, "p", "link.jsonl")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(projects, "p"), filepath.Join(projects, "linked-project")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(projects, "p", "dir.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}
	report := runBackfill(t, projects, state, true)
	wantCounts(t, report, map[string]int{"scanned": 1, "attributed": 1})
	if report.Rows[0].TranscriptPath != top {
		t.Fatalf("rows = %#v", report.Rows)
	}
}

func TestBackfillMissingProjectsDirScansNothing(t *testing.T) {
	state := t.TempDir()
	report := runBackfill(t, filepath.Join(t.TempDir(), "absent"), state, false)
	wantCounts(t, report, map[string]int{})
	if body, _ := json.Marshal(report); !strings.Contains(string(body), `"rows":[]`) {
		t.Fatalf("report = %s, want an empty rows array", body)
	}
	if _, err := os.Stat(Path(state)); !os.IsNotExist(err) {
		t.Fatalf("a run with nothing to add created the history file: %v", err)
	}
}

func TestBackfillNeverDuplicatesObservedOrCurrentSessionsAndRerunAppendsNothing(t *testing.T) {
	projects, state := t.TempDir(), t.TempDir()
	writeTranscript(t, projects, "p", "observed-elsewhere", userLine(ts1, frameText("agent-a")))
	writeTranscript(t, projects, "p", "current-in-registry", userLine(ts1, frameText("agent-a")))
	writeTranscript(t, projects, "p", "fresh", userLine(ts1, frameText("agent-a")))
	// The observation belongs to another Agent: any Agent's observation wins.
	if err := Append(state, observed(t, "agent-other", "observed-elsewhere", t0)); err != nil {
		t.Fatal(err)
	}
	current, _ := RecordFor("agent-b", claudeRef("current-in-registry", "", t0), SourceCurrent)

	report := runBackfill(t, projects, state, false, current)
	wantCounts(t, report, map[string]int{"scanned": 3, "attributed": 1, "skippedObserved": 2})
	if len(report.Rows) != 1 || report.Rows[0].SessionID != "fresh" {
		t.Fatalf("rows = %#v", report.Rows)
	}
	before, err := os.ReadFile(Path(state))
	if err != nil {
		t.Fatal(err)
	}

	again := runBackfill(t, projects, state, false, current)
	wantCounts(t, again, map[string]int{"scanned": 3, "alreadyEstimated": 1, "skippedObserved": 2})
	after, err := os.ReadFile(Path(state))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || len(again.Rows) != 0 {
		t.Fatalf("the second run changed the history:\nbefore %s\nafter  %s", before, after)
	}
	read, _ := Read(state, "")
	if len(read.Records) != 2 {
		t.Fatalf("history = %#v, want the observed line and one estimated line", read.Records)
	}
}

func TestBackfillEstimatedRowDefersToALaterObservation(t *testing.T) {
	projects, state := t.TempDir(), t.TempDir()
	writeTranscript(t, projects, "p", "s", userLine(ts1, frameText("agent-a")))
	runBackfill(t, projects, state, false)
	// The session is later observed: a rerun counts it observed, not estimated.
	if err := Append(state, observed(t, "agent-a", "s", t0)); err != nil {
		t.Fatal(err)
	}
	report := runBackfill(t, projects, state, false)
	wantCounts(t, report, map[string]int{"scanned": 1, "skippedObserved": 1})
	result, err := List(state, agentWith("agent-a", nil))
	if err != nil || len(result.Sessions) != 1 || result.Sessions[0].Source != SourceObserved {
		t.Fatalf("sessions = %#v (err %v), want one observed row", result.Sessions, err)
	}
}

func TestBackfillDryRunWritesNothing(t *testing.T) {
	projects := t.TempDir()
	state := filepath.Join(t.TempDir(), "state", "projmux")
	writeTranscript(t, projects, "p", "s", userLine(ts1, frameText("agent-a")))
	dry := runBackfill(t, projects, state, true)
	if _, err := os.Stat(filepath.Dir(state)); !os.IsNotExist(err) {
		t.Fatalf("dry run created the state directory: %v", err)
	}
	real := runBackfill(t, projects, state, false)
	dry.DryRun = true
	real.DryRun = true
	if !reflect.DeepEqual(dry, real) {
		t.Fatalf("dry run reported %#v, the real run %#v", dry, real)
	}
}

type fileFingerprint struct {
	mode    fs.FileMode
	size    int64
	modTime time.Time
	sum     [32]byte
}

func fingerprint(t *testing.T, root string) map[string]fileFingerprint {
	t.Helper()
	out := map[string]fileFingerprint{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		print := fileFingerprint{mode: info.Mode(), size: info.Size(), modTime: info.ModTime()}
		if info.Mode().IsRegular() {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			print.sum = sha256.Sum256(body)
		}
		out[path] = print
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestBackfillLeavesTranscriptsUnchangedAndNeedsNoWriteAccess(t *testing.T) {
	projects, state := t.TempDir(), t.TempDir()
	writeTranscript(t, projects, "p", "one", userLine(ts1, frameText("agent-a")), userLine(ts2, "bye"))
	writeTranscript(t, projects, "p", "two", userLine(ts1, frameText("agent-a")), userLine(ts2, frameText("agent-b")))
	writeTranscript(t, projects, "q", "three", "garbage", userLine(ts1, "no frame"))
	// Read-only files in read-only directories: any write attempt would fail.
	var dirs []string
	err := filepath.WalkDir(projects, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			dirs = append(dirs, path)
			return nil
		}
		return os.Chmod(path, 0o400)
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(dirs)
	for _, dir := range dirs {
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		slices.Reverse(dirs)
		for _, dir := range dirs {
			_ = os.Chmod(dir, 0o700)
		}
	})
	before := fingerprint(t, projects)

	report := runBackfill(t, projects, state, false)
	wantCounts(t, report, map[string]int{"scanned": 3, "attributed": 1, "ambiguous": 1, "noFrame": 1, "malformedLines": 1})
	if after := fingerprint(t, projects); !reflect.DeepEqual(before, after) {
		t.Fatalf("transcripts changed:\nbefore %#v\nafter  %#v", before, after)
	}
}

func TestBackfillReportJSONKeysAreFixed(t *testing.T) {
	projects, state := t.TempDir(), t.TempDir()
	writeTranscript(t, projects, "p", "s", userLine(ts1, frameText("agent-a")))
	report := runBackfill(t, projects, state, true)
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for key := range fields {
		keys = append(keys, key)
	}
	want := []string{"dryRun", "projectsDir", "historyPath", "scanned", "attributed", "alreadyEstimated", "skippedObserved",
		"ambiguous", "noFrame", "unreadable", "malformedLines", "malformedFrames", "rows"}
	if !sameSet(keys, want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	var rows []map[string]any
	if err := json.Unmarshal(fields["rows"], &rows); err != nil || len(rows) != 1 {
		t.Fatalf("rows = %s", fields["rows"])
	}
	var rowKeys []string
	for key := range rows[0] {
		rowKeys = append(rowKeys, key)
	}
	if !sameSet(rowKeys, []string{"agentUID", "lastRecordAt", "observedAt", "provider", "sessionId", "source", "transcriptPath"}) {
		t.Fatalf("row keys = %v", rowKeys)
	}
	if rows[0]["source"] != "estimated" || rows[0]["observedAt"] != "2026-08-01T10:00:00.123Z" || rows[0]["lastRecordAt"] != "2026-08-01T10:00:00.123Z" {
		t.Fatalf("row = %v", rows[0])
	}
}

func TestClaudeProjectsDirHonorsClaudeConfigDir(t *testing.T) {
	for _, tc := range []struct{ configDir, home, want string }{
		{"", "/home/u", "/home/u/.claude/projects"},
		{"  ", "/home/u", "/home/u/.claude/projects"},
		{"/cfg/claude", "/home/u", "/cfg/claude/projects"},
	} {
		got, err := ClaudeProjectsDir(tc.configDir, tc.home)
		if err != nil || got != tc.want {
			t.Errorf("ClaudeProjectsDir(%q, %q) = %q, %v; want %q", tc.configDir, tc.home, got, err, tc.want)
		}
	}
	if _, err := ClaudeProjectsDir("", ""); err == nil {
		t.Error("no home and no CLAUDE_CONFIG_DIR resolved a directory")
	}
}

func TestBackfillReleasesTheLockBeforeItsFsync(t *testing.T) {
	projects, state := t.TempDir(), t.TempDir()
	writeTranscript(t, projects, "-src-app", "sess-1", userLine(ts1, frameText("agent-a")))
	appended := observed(t, "agent-b", "B", t0)
	entered, release := stallFirstSync(t, nil)
	type outcome struct {
		report BackfillReport
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		report, err := Backfill(BackfillOptions{ProjectsDir: projects, StateDir: state})
		done <- outcome{report, err}
	}()
	waitFor(t, entered, "Backfill to reach its fsync")

	// Backfill is inside its fsync and stays there until release. An Append
	// must not queue behind it: holding the lock across the fsync fails this
	// one with a lock error after lockWait.
	if err := Append(state, appended); err != nil {
		t.Fatalf("Append while Backfill's fsync is stalled: %v", err)
	}
	release()
	got := waitFor(t, done, "the stalled Backfill to return")
	if got.err != nil {
		t.Fatalf("Backfill: %v", got.err)
	}
	wantCounts(t, got.report, map[string]int{"scanned": 1, "attributed": 1})
	read, err := Read(state, "")
	if err != nil {
		t.Fatal(err)
	}
	if rows := sessionSources(read.Records); read.Corrupt != 0 || !reflect.DeepEqual(rows, []string{"sess-1/estimated", "B/observed"}) {
		t.Fatalf("history = %v with %d corrupt lines, want the estimated row then the appended one", rows, read.Corrupt)
	}
}
