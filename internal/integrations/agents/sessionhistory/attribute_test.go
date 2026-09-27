package sessionhistory

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func runAttribute(t *testing.T, state string, reg *coremetadata.Registry, dryRun bool) AttributeReport {
	t.Helper()
	report, err := Attribute(AttributeOptions{StateDir: state, Registry: reg, DryRun: dryRun})
	if err != nil {
		t.Fatalf("Attribute(dryRun=%v): %v", dryRun, err)
	}
	return report
}

func attributeCounts(r AttributeReport) [4]int {
	return [4]int{r.Scanned, r.Attributed, r.AlreadyAttributed, r.Unresolved}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestAttributePersistsTheRegistryAffiliationThatOutlivesTheAgent(t *testing.T) {
	r := (&affiliationRegistry{}).project("p1").window("w1", "p1").
		agent("a-live", "w1", ProviderClaude, claudeRef("S-current", "/t/S-current.jsonl", t0.Add(time.Hour)))
	state := t.TempDir()
	appendAll(t, state, observed(t, "a-live", "S-old", t0)) // an old-format row

	report := runAttribute(t, state, &r.reg, false)
	if got, want := attributeCounts(report), [4]int{2, 2, 0, 0}; got != want {
		t.Fatalf("counts = %v, want %v (%+v)", got, want, report)
	}
	for i, want := range []string{"S-old", "S-current"} {
		row := report.Rows[i]
		if row.SessionID != want || row.AgentUID != "a-live" || row.ProjectUID != "p1" || row.WindowUID != "w1" ||
			row.AgentName != "name-a-live" || row.AffiliationBasis != BasisRegistry || row.Source != SourceObserved {
			t.Fatalf("row %d = %+v", i, row)
		}
	}
	if read := readSessionRows(t, state); len(read) != 3 || !reflect.DeepEqual(read[1:], report.Rows) {
		t.Fatalf("file rows = %+v, want the old row then %+v", read, report.Rows)
	}

	// The Agent is deleted: its sessions stay listed by the persisted
	// affiliation, not re-resolved.
	deleted := (&affiliationRegistry{}).project("p1").window("w1", "p1")
	result, err := ListProject(state, &deleted.reg, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Sessions) != 2 || result.Unattributed != 0 || result.Ambiguous != 0 {
		t.Fatalf("result = %+v", result)
	}
	wantAgents := []ProjectSessionAgent{{AgentUID: "a-live", AgentName: "name-a-live", WindowUID: "w1", InRegistry: false, Basis: BasisRegistry}}
	for i, want := range []string{"S-old", "S-current"} {
		if got := result.Sessions[i]; got.SessionID != want || !reflect.DeepEqual(got.Agents, wantAgents) {
			t.Fatalf("session %d = %+v, want %s by registry", i, got, want)
		}
	}
	// Without the attribution the same deletion leaves them unattributed.
	bare := t.TempDir()
	appendAll(t, bare, observed(t, "a-live", "S-old", t0))
	if before, err := ListProject(bare, &deleted.reg, "p1"); err != nil || len(before.Sessions) != 0 || before.Unattributed != 1 {
		t.Fatalf("unattributed listing = %+v, %v", before, err)
	}
}

func readSessionRows(t *testing.T, state string) []Record {
	t.Helper()
	read, err := Read(state, "")
	if err != nil {
		t.Fatal(err)
	}
	return read.Records
}

func TestAttributeSecondRunAppendsNothingAndLeavesOtherRowsUntouched(t *testing.T) {
	r := (&affiliationRegistry{}).project("p1").window("w1", "p1").window("w-orphan", "p-gone").
		agent("a-live", "w1", ProviderClaude, nil).
		agent("a-orphan", "w-orphan", ProviderCodex, codexRef("T-orphan", t0))
	state := t.TempDir()
	appendAll(t, state,
		recordedIn(observed(t, "a-live", "S-recorded", t0), "p-other", "w-other", "snapshot"),
		observed(t, "a-live", "S-old", t0.Add(time.Minute)),
		observed(t, "a-deleted", "S-deleted", t0.Add(2*time.Minute)),
		observed(t, "a-live", "S-recorded", t0.Add(3*time.Minute)), // a bare row of an attributed key
	)
	if err := appendRaw(state, "not json\n"); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, Path(state))

	first := runAttribute(t, state, &r.reg, false)
	// S-recorded, S-old, S-deleted from the file; T-orphan only current.
	if got, want := attributeCounts(first), [4]int{4, 1, 1, 2}; got != want || first.CorruptLines != 1 {
		t.Fatalf("first counts = %v (corrupt %d), want %v", got, first.CorruptLines, want)
	}
	if len(first.Rows) != 1 || first.Rows[0].SessionID != "S-old" || first.Rows[0].ProjectUID != "p1" {
		t.Fatalf("first rows = %+v", first.Rows)
	}
	after := readFile(t, Path(state))
	if !bytes.HasPrefix(after, before) || len(after) == len(before) {
		t.Fatalf("the existing bytes are not a prefix of the new file")
	}
	if got := bytes.Count(after[len(before):], []byte("\n{")); got != 1 {
		t.Fatalf("appended %d lines, want 1", got)
	}

	second := runAttribute(t, state, &r.reg, false)
	if got, want := attributeCounts(second), [4]int{4, 0, 2, 2}; got != want || len(second.Rows) != 0 || second.Rows == nil {
		t.Fatalf("second = %+v, want counts %v and no rows", second, want)
	}
	if again := readFile(t, Path(state)); !bytes.Equal(again, after) {
		t.Fatal("the second run changed the file")
	}
	// The key that already had a projectUID keeps it: no row names p1 for it.
	for _, row := range readSessionRows(t, state) {
		if row.SessionID == "S-recorded" && row.ProjectUID == "p1" {
			t.Fatalf("an already attributed key was re-attributed: %+v", row)
		}
		if row.AgentUID == "a-deleted" && row.ProjectUID != "" {
			t.Fatalf("a deleted Agent's row was attributed: %+v", row)
		}
	}
}

func TestAttributeDryRunWritesNothingAndReportsTheRealRun(t *testing.T) {
	r := (&affiliationRegistry{}).project("p1").window("w1", "p1").
		agent("a-live", "w1", ProviderCodex, codexRef("T-current", t0.Add(time.Hour)))

	// A missing file is not created, not even its directory.
	missing := filepath.Join(t.TempDir(), "state", "projmux")
	dry := runAttribute(t, missing, &r.reg, true)
	if _, err := os.Stat(filepath.Dir(missing)); !os.IsNotExist(err) {
		t.Fatalf("dry run created the state directory: %v", err)
	}
	if got, want := attributeCounts(dry), [4]int{1, 1, 0, 0}; got != want || !dry.DryRun {
		t.Fatalf("dry = %+v", dry)
	}

	state := t.TempDir()
	appendAll(t, state, observed(t, "a-live", "S-old", t0), observed(t, "a-gone", "S-gone", t0))
	before := readFile(t, Path(state))
	dry = runAttribute(t, state, &r.reg, true)
	if !bytes.Equal(readFile(t, Path(state)), before) {
		t.Fatal("the dry run changed the file")
	}
	real := runAttribute(t, state, &r.reg, false)
	dry.DryRun = false
	if !reflect.DeepEqual(dry, real) {
		t.Fatalf("dry run reported %#v, the real run %#v", dry, real)
	}
	if got, want := attributeCounts(real), [4]int{3, 2, 0, 1}; got != want {
		t.Fatalf("counts = %v, want %v", got, want)
	}
}

func TestAttributeWithNothingToAppendCreatesNothing(t *testing.T) {
	// The Agent's chain is broken, so its current ref cannot be attributed.
	r := (&affiliationRegistry{}).window("w-orphan", "p-gone").
		agent("a-orphan", "w-orphan", ProviderClaude, claudeRef("S", "/t/S.jsonl", t0))
	state := filepath.Join(t.TempDir(), "state", "projmux")
	report := runAttribute(t, state, &r.reg, false)
	if got, want := attributeCounts(report), [4]int{1, 0, 0, 1}; got != want {
		t.Fatalf("counts = %v, want %v", got, want)
	}
	if _, err := os.Stat(filepath.Dir(state)); !os.IsNotExist(err) {
		t.Fatalf("a run with nothing to append created the state directory: %v", err)
	}

	// A missing file whose current refs do attribute is created.
	ok := (&affiliationRegistry{}).project("p1").window("w1", "p1").
		agent("a-live", "w1", ProviderClaude, claudeRef("S", "/t/S.jsonl", t0))
	if report := runAttribute(t, state, &ok.reg, false); report.Attributed != 1 || len(readSessionRows(t, state)) != 1 {
		t.Fatalf("report = %+v", report)
	}
}

func TestAttributeRowSourceAndTimesFollowMerge(t *testing.T) {
	last := t0.Add(30 * time.Minute)
	estimated := observed(t, "a-live", "S-merged", t0)
	estimated.Source, estimated.LastRecordAt, estimated.TranscriptPath = SourceEstimated, &last, "/t/estimated.jsonl"
	later := observed(t, "a-live", "S-merged", t0.Add(time.Minute))
	onlyEstimated := observed(t, "a-live", "S-estimated", t0.Add(2*time.Minute))
	onlyEstimated.Source = SourceEstimated
	// The current ref names S-estimated later than its file row.
	r := (&affiliationRegistry{}).project("p1").window("w1", "p1").
		agent("a-live", "w1", ProviderClaude, claudeRef("S-estimated", "/t/current.jsonl", t0.Add(time.Hour)))
	state := t.TempDir()
	appendAll(t, state, estimated, later, onlyEstimated)

	report := runAttribute(t, state, &r.reg, false)
	if len(report.Rows) != 2 {
		t.Fatalf("rows = %+v", report.Rows)
	}
	merged, current := report.Rows[0], report.Rows[1]
	if merged.SessionID != "S-merged" || merged.Source != SourceObserved || !merged.ObservedAt.Equal(t0.Add(time.Minute)) ||
		merged.TranscriptPath != "/t/S-merged.jsonl" || merged.LastRecordAt == nil || !merged.LastRecordAt.Equal(last) {
		t.Fatalf("merged row = %+v", merged)
	}
	// current is never written: the file's highest source is kept.
	if current.SessionID != "S-estimated" || current.Source != SourceEstimated || !current.ObservedAt.Equal(t0.Add(time.Hour)) ||
		current.TranscriptPath != "/t/current.jsonl" {
		t.Fatalf("row merged with the current ref = %+v", current)
	}
	for _, row := range readSessionRows(t, state) {
		if row.Source == SourceCurrent {
			t.Fatalf("a current row was written: %+v", row)
		}
	}
}

func TestAttributeReportJSONKeysAreFixed(t *testing.T) {
	empty, err := json.Marshal(AttributeReport{HistoryPath: "/s/h.jsonl", Rows: []Record{}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"dryRun":false,"historyPath":"/s/h.jsonl","scanned":0,"attributed":0,"alreadyAttributed":0,"unresolved":0,"corruptLines":0,"rows":[]}`; string(empty) != want {
		t.Fatalf("empty report = %s, want %s", empty, want)
	}

	r := (&affiliationRegistry{}).project("p1").window("w1", "p1").
		agent("a-live", "w1", ProviderClaude, claudeRef("S", "/t/S.jsonl", t0))
	report := runAttribute(t, t.TempDir(), &r.reg, true)
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(envelope["rows"], &rows); err != nil || len(rows) != 1 {
		t.Fatalf("rows = %s", envelope["rows"])
	}
	want := []string{"affiliationBasis", "agentName", "agentUID", "observedAt", "projectUID", "provider", "sessionId", "source", "transcriptPath", "windowUID"}
	if got := jsonKeys(t, rows[0]); !slices.Equal(got, want) {
		t.Fatalf("row keys = %v, want %v", got, want)
	}
	var row map[string]any
	if err := json.Unmarshal(rows[0], &row); err != nil {
		t.Fatal(err)
	}
	// A current-only conversation is written as observed.
	if row["affiliationBasis"] != "registry" || row["source"] != "observed" {
		t.Fatalf("row = %s", rows[0])
	}
}
