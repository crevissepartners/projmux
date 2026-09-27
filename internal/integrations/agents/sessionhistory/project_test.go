package sessionhistory

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// affiliationRegistry is a Registry of Projects, Windows, and Agents linked
// only by ownerRef, which is all ResolveAffiliation reads.
type affiliationRegistry struct{ reg coremetadata.Registry }

func (r *affiliationRegistry) project(uid string) *affiliationRegistry {
	var project coremetadata.Project
	project.Metadata.UID, project.Metadata.Name = uid, "name-"+uid
	r.reg.Projects = append(r.reg.Projects, project)
	return r
}

func (r *affiliationRegistry) window(uid, projectUID string) *affiliationRegistry {
	var window coremetadata.Window
	window.Metadata.UID, window.Metadata.Name = uid, "name-"+uid
	window.Metadata.OwnerRef = &coremetadata.OwnerRef{Kind: coremetadata.KindProject, UID: projectUID}
	r.reg.Windows = append(r.reg.Windows, window)
	return r
}

func (r *affiliationRegistry) agent(uid, windowUID, provider string, ref *coremetadata.AgentSessionRef) *affiliationRegistry {
	var agent coremetadata.Agent
	agent.Metadata.UID, agent.Metadata.Name = uid, "name-"+uid
	agent.Metadata.OwnerRef = &coremetadata.OwnerRef{Kind: coremetadata.KindWindow, UID: windowUID}
	agent.Spec.Provider = provider
	agent.Status.SessionRef = ref
	r.reg.Agents = append(r.reg.Agents, agent)
	return r
}

func TestResolveAffiliationSetsEachKeyOnlyAsFarAsTheChainHolds(t *testing.T) {
	r := (&affiliationRegistry{}).project("p1").window("w1", "p1").window("w-orphan", "p-gone").
		agent("a-full", "w1", ProviderClaude, nil).
		agent("a-orphan-window", "w-orphan", ProviderClaude, nil).
		agent("a-no-window", "w-gone", ProviderClaude, nil)
	var unowned coremetadata.Agent
	unowned.Metadata.UID, unowned.Metadata.Name = "a-unowned", "name-a-unowned"
	r.reg.Agents = append(r.reg.Agents, unowned)

	for uid, want := range map[string]Affiliation{
		"a-full":          {ProjectUID: "p1", WindowUID: "w1", AgentName: "name-a-full"},
		"a-orphan-window": {WindowUID: "w-orphan", AgentName: "name-a-orphan-window"},
		"a-no-window":     {AgentName: "name-a-no-window"},
		"a-unowned":       {AgentName: "name-a-unowned"},
		"a-gone":          {},
		"":                {},
	} {
		if got := ResolveAffiliation(&r.reg, uid); got != want {
			t.Errorf("ResolveAffiliation(%q) = %+v, want %+v", uid, got, want)
		}
	}
	if got := ResolveAffiliation(nil, "a-full"); got != (Affiliation{}) {
		t.Errorf("nil Registry = %+v, want empty", got)
	}
}

func TestObservedRecordForAttachesAffiliationAndABrokenChainStillWritesTheRow(t *testing.T) {
	r := (&affiliationRegistry{}).project("p1").window("w1", "p1").agent("a-full", "w1", ProviderClaude, nil).
		agent("a-no-window", "w-gone", ProviderClaude, nil)
	ref := claudeRef("S", "/t/S.jsonl", t0)

	full, ok := ObservedRecordFor(&r.reg, "a-full", ref)
	if !ok || full.Source != SourceObserved || full.ProjectUID != "p1" || full.WindowUID != "w1" || full.AgentName != "name-a-full" {
		t.Fatalf("full chain row = %+v (ok=%v)", full, ok)
	}
	state := t.TempDir()
	for _, uid := range []string{"a-no-window", "a-deleted"} {
		row, ok := ObservedRecordFor(&r.reg, uid, ref)
		if !ok {
			t.Fatalf("%s: a broken chain refused the row", uid)
		}
		if row.ProjectUID != "" || row.WindowUID != "" {
			t.Fatalf("%s: broken chain guessed an affiliation: %+v", uid, row)
		}
		if err := Append(state, row); err != nil {
			t.Fatalf("%s: append: %v", uid, err)
		}
	}
	read, err := Read(state, "")
	if err != nil || read.Corrupt != 0 || len(read.Records) != 2 {
		t.Fatalf("read = %+v, %v", read, err)
	}
	if got := read.Records[0]; got.AgentName != "name-a-no-window" || got.ProjectUID != "" || got.WindowUID != "" {
		t.Fatalf("agent without its Window = %+v", got)
	}
	if got := read.Records[1]; got.AgentName != "" || got.ProjectUID != "" || got.WindowUID != "" || got.SessionID != "S" {
		t.Fatalf("missing agent = %+v", got)
	}
	body, err := json.Marshal(read.Records[1])
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"projectUID", "windowUID", "agentName"} {
		if bytes.Contains(body, []byte(`"`+key+`"`)) {
			t.Fatalf("an empty %s was written: %s", key, body)
		}
	}
	if _, ok := ObservedRecordFor(&r.reg, "a-full", nil); ok {
		t.Fatal("ObservedRecordFor accepted a nil ref")
	}
}

func TestRecordAffiliationKeysFollowTheExistingKeys(t *testing.T) {
	row := observed(t, "agent-1", "A", t0)
	row.ProjectUID, row.WindowUID, row.AgentName = "p1", "w1", "alpha"
	body, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"agentUID":"agent-1","provider":"claude","sessionId":"A","transcriptPath":"/t/A.jsonl",` +
		`"observedAt":"2026-09-25T09:00:00Z","source":"observed","projectUID":"p1","windowUID":"w1","agentName":"alpha"}`
	if string(body) != want {
		t.Fatalf("row =\n %s\nwant\n %s", body, want)
	}

	// affiliationBasis, written only by Attribute, follows agentName.
	row.AffiliationBasis = BasisRegistry
	body, err = json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	want = strings.TrimSuffix(want, "}") + `,"affiliationBasis":"registry"}`
	if string(body) != want {
		t.Fatalf("attributed row =\n %s\nwant\n %s", body, want)
	}
}

func TestMergeCarriesAffiliationBasisWithTheKeptProjectUID(t *testing.T) {
	recorded := recordedIn(observed(t, "agent-1", "A", t0), "p-old", "w-old", "old")
	attributed := recordedIn(observed(t, "agent-1", "A", t0.Add(time.Minute)), "p-new", "w-new", "new")
	attributed.AffiliationBasis = BasisRegistry
	// A later row naming only an agentName keeps the kept projectUID's basis.
	renamed := observed(t, "agent-1", "A", t0.Add(2*time.Minute))
	renamed.AgentName = "renamed"

	rows := Merge([]Record{recorded, attributed, renamed}, nil)
	if len(rows) != 1 || rows[0].ProjectUID != "p-new" || rows[0].AffiliationBasis != BasisRegistry || rows[0].AgentName != "renamed" {
		t.Fatalf("merged = %+v", rows)
	}
	// A later recorded projectUID replaces the basis with its own (empty).
	later := recordedIn(observed(t, "agent-1", "A", t0.Add(3*time.Minute)), "p-later", "w-later", "later")
	rows = Merge([]Record{attributed, later}, nil)
	if rows[0].ProjectUID != "p-later" || rows[0].AffiliationBasis != "" {
		t.Fatalf("merged = %+v", rows[0])
	}
}

func TestMergeKeepsTheLatestNonEmptyAffiliationOfEachKey(t *testing.T) {
	first := observed(t, "agent-1", "A", t0)
	first.ProjectUID, first.WindowUID, first.AgentName = "p1", "w1", "old-name"
	renamed := observed(t, "agent-1", "A", t0.Add(time.Minute))
	renamed.AgentName = "new-name"
	bare := observed(t, "agent-1", "A", t0.Add(2*time.Minute)) // an old-format line
	estimated := observed(t, "agent-1", "A", t0.Add(3*time.Minute))
	estimated.Source = SourceEstimated
	current, _ := RecordFor("agent-1", claudeRef("A", "", t0.Add(4*time.Minute)), SourceCurrent)

	rows := Merge([]Record{first, renamed, bare, estimated}, &current)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	got := rows[0]
	if got.ProjectUID != "p1" || got.WindowUID != "w1" || got.AgentName != "new-name" || got.Source != SourceCurrent ||
		!got.ObservedAt.Equal(t0.Add(4*time.Minute)) {
		t.Fatalf("merged = %+v", got)
	}

	// Input order does not decide: an older row later in the input does not
	// override a newer one's value.
	rows = Merge([]Record{renamed, first}, nil)
	if rows[0].AgentName != "new-name" || rows[0].ProjectUID != "p1" {
		t.Fatalf("merged out of order = %+v", rows[0])
	}
}

func codexRef(threadID string, at time.Time) *coremetadata.AgentSessionRef {
	return &coremetadata.AgentSessionRef{Provider: ProviderCodex, ObservedAt: at, Codex: &coremetadata.CodexSessionRef{ThreadID: threadID}}
}

func appendAll(t *testing.T, state string, rows ...Record) {
	t.Helper()
	for _, row := range rows {
		if err := Append(state, row); err != nil {
			t.Fatal(err)
		}
	}
}

func recordedIn(row Record, projectUID, windowUID, agentName string) Record {
	row.ProjectUID, row.WindowUID, row.AgentName = projectUID, windowUID, agentName
	return row
}

func TestListProjectAttributesOldRowsThroughTheCurrentRegistryOnly(t *testing.T) {
	r := (&affiliationRegistry{}).project("p1").window("w1", "p1").
		agent("a-live", "w1", ProviderClaude, claudeRef("S-current", "/t/S-current.jsonl", t0.Add(time.Hour)))
	state := t.TempDir()
	// Old-format rows: no affiliation keys.
	appendAll(t, state,
		observed(t, "a-live", "S-old", t0),
		observed(t, "a-deleted", "S-orphan", t0.Add(time.Minute)),
	)

	result, err := ListProject(state, &r.reg, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Unattributed != 1 || result.Ambiguous != 0 || result.CorruptLines != 0 || len(result.Sessions) != 2 {
		t.Fatalf("result = %+v", result)
	}
	for i, want := range []string{"S-old", "S-current"} {
		session := result.Sessions[i]
		wantAgents := []ProjectSessionAgent{{AgentUID: "a-live", AgentName: "name-a-live", WindowUID: "w1", InRegistry: true, Basis: BasisRegistry}}
		if session.SessionID != want || !reflect.DeepEqual(session.Agents, wantAgents) {
			t.Fatalf("session %d = %+v, want %s by registry", i, session, want)
		}
	}
	if result.Sessions[1].Source != SourceCurrent || result.Sessions[0].Source != SourceObserved {
		t.Fatalf("sources = %s, %s", result.Sessions[0].Source, result.Sessions[1].Source)
	}

	other, err := ListProject(state, &r.reg, "p-other")
	if err != nil || len(other.Sessions) != 0 || other.Sessions == nil || other.Unattributed != 1 {
		t.Fatalf("another project = %+v, %v", other, err)
	}
}

func TestListProjectListsADeletedAgentByItsRecordedAffiliation(t *testing.T) {
	r := (&affiliationRegistry{}).project("p1").window("w1", "p1").agent("a-live", "w1", ProviderClaude, nil)
	state := t.TempDir()
	deleted := recordedIn(observed(t, "a-deleted", "S", t0), "p1", "w-gone", "reviewer")
	// A later, bare line of the same live agent in the same session joins it
	// by registry; the recorded row of the same agent wins for that agent.
	live := observed(t, "a-live", "S", t0.Add(time.Minute))
	liveRecorded := recordedIn(observed(t, "a-live", "S", t0.Add(2*time.Minute)), "p1", "w1", "snapshot-name")
	appendAll(t, state, deleted, live, liveRecorded)

	result, err := ListProject(state, &r.reg, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Sessions) != 1 || result.Unattributed != 0 || result.Ambiguous != 0 {
		t.Fatalf("result = %+v", result)
	}
	want := []ProjectSessionAgent{
		{AgentUID: "a-deleted", AgentName: "reviewer", WindowUID: "w-gone", InRegistry: false, Basis: BasisRecorded},
		{AgentUID: "a-live", AgentName: "snapshot-name", WindowUID: "w1", InRegistry: true, Basis: BasisRecorded},
	}
	if got := result.Sessions[0].Agents; !reflect.DeepEqual(got, want) {
		t.Fatalf("agents = %+v\nwant %+v", got, want)
	}
	if got := result.Sessions[0]; !got.ObservedAt.Equal(t0.Add(2*time.Minute)) || got.TranscriptPath != "/t/S.jsonl" || got.Provider != ProviderClaude {
		t.Fatalf("session = %+v", got)
	}
}

func TestListProjectGroupsASessionByProviderAndSessionID(t *testing.T) {
	r := (&affiliationRegistry{}).project("p1").project("p2").window("w1", "p1").window("w2", "p2").
		agent("a1", "w1", ProviderClaude, nil).agent("b1", "w1", ProviderClaude, nil).
		agent("c2", "w2", ProviderClaude, nil).
		agent("x-codex", "w1", ProviderCodex, codexRef("S-shared", t0.Add(9*time.Minute)))
	state := t.TempDir()
	lastRecord := t0.Add(30 * time.Minute)
	estimated := observed(t, "b1", "S-same", t0.Add(time.Minute))
	estimated.Source, estimated.LastRecordAt, estimated.TranscriptPath = SourceEstimated, &lastRecord, ""
	appendAll(t, state,
		// Two Agents of one Project in one session: one line, two agents.
		observed(t, "a1", "S-same", t0),
		estimated,
		// The same session in two Projects: ambiguous, listed under neither.
		observed(t, "a1", "S-split", t0.Add(2*time.Minute)),
		recordedIn(observed(t, "c2", "S-split", t0.Add(3*time.Minute)), "p2", "w2", "name-c2"),
		// The same id under another provider is another session.
		observed(t, "a1", "S-shared", t0.Add(4*time.Minute)),
	)
	// A row of an unsupported provider is ignored.
	if err := appendRaw(state, `{"agentUID":"a1","provider":"antigravity","sessionId":"S-other","observedAt":"2026-09-25T09:00:00Z","source":"observed"}`+"\n"); err != nil {
		t.Fatal(err)
	}

	result, err := ListProject(state, &r.reg, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Ambiguous != 1 || result.Unattributed != 0 {
		t.Fatalf("counts = %+v", result)
	}
	var got []string
	for _, session := range result.Sessions {
		got = append(got, session.Provider+"/"+session.SessionID)
	}
	if want := []string{"claude/S-same", "claude/S-shared", "codex/S-shared"}; !slices.Equal(got, want) {
		t.Fatalf("sessions = %v, want %v", got, want)
	}
	same := result.Sessions[0]
	if len(same.Agents) != 2 || same.Agents[0].AgentUID != "a1" || same.Agents[1].AgentUID != "b1" ||
		same.Source != SourceObserved || same.LastRecordAt == nil || !same.LastRecordAt.Equal(lastRecord) ||
		!same.ObservedAt.Equal(t0.Add(time.Minute)) || same.TranscriptPath != "/t/S-same.jsonl" {
		t.Fatalf("S-same = %+v", same)
	}
	if codex := result.Sessions[2]; codex.Source != SourceCurrent || codex.TranscriptPath != "" || len(codex.Agents) != 1 || codex.Agents[0].AgentUID != "x-codex" {
		t.Fatalf("codex session = %+v", codex)
	}
	p2, err := ListProject(state, &r.reg, "p2")
	if err != nil || len(p2.Sessions) != 0 || p2.Ambiguous != 1 {
		t.Fatalf("p2 = %+v, %v", p2, err)
	}
}

func TestListProjectCountsCorruptLinesAndReadsAMissingFileAsEmpty(t *testing.T) {
	r := (&affiliationRegistry{}).project("p1")
	result, err := ListProject(t.TempDir(), &r.reg, "p1")
	if err != nil || result.Sessions == nil || len(result.Sessions) != 0 || result.CorruptLines != 0 {
		t.Fatalf("empty = %+v, %v", result, err)
	}
	state := t.TempDir()
	appendAll(t, state, observed(t, "a-gone", "S", t0))
	if err := appendRaw(state, "not json\n"); err != nil {
		t.Fatal(err)
	}
	result, err = ListProject(state, &r.reg, "p1")
	if err != nil || result.CorruptLines != 1 || result.Unattributed != 1 {
		t.Fatalf("corrupt = %+v, %v", result, err)
	}
}

func appendRaw(state, text string) error {
	file, err := os.OpenFile(Path(state), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.WriteString(text)
	return err
}

func jsonKeys(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestProjectResultJSONKeysAreFixed(t *testing.T) {
	empty, err := json.Marshal(ProjectResult{ProjectUID: "p1", Sessions: []ProjectSession{}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"projectUID":"p1","sessions":[],"unattributed":0,"ambiguous":0,"corruptLines":0}`; string(empty) != want {
		t.Fatalf("empty result = %s, want %s", empty, want)
	}

	last := t0.Add(time.Hour)
	body, err := json.Marshal(ProjectResult{ProjectUID: "p1", Sessions: []ProjectSession{{
		Provider: ProviderClaude, SessionID: "S", TranscriptPath: "/t/S.jsonl", ObservedAt: t0, LastRecordAt: &last,
		Source: SourceEstimated, Agents: []ProjectSessionAgent{{AgentUID: "a", AgentName: "n", WindowUID: "w", Basis: BasisRegistry}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	var sessions []json.RawMessage
	if err := json.Unmarshal(envelope["sessions"], &sessions); err != nil || len(sessions) != 1 {
		t.Fatalf("sessions = %s", envelope["sessions"])
	}
	var session map[string]json.RawMessage
	if err := json.Unmarshal(sessions[0], &session); err != nil {
		t.Fatal(err)
	}
	var agents []json.RawMessage
	if err := json.Unmarshal(session["agents"], &agents); err != nil || len(agents) != 1 {
		t.Fatalf("agents = %s", session["agents"])
	}
	for _, check := range []struct {
		what string
		raw  json.RawMessage
		want []string
	}{
		{"session", sessions[0], []string{"agents", "lastRecordAt", "observedAt", "provider", "sessionId", "source", "transcriptPath"}},
		{"agent", agents[0], []string{"agentName", "agentUID", "basis", "inRegistry", "windowUID"}},
	} {
		if got := jsonKeys(t, check.raw); !slices.Equal(got, check.want) {
			t.Fatalf("%s keys = %v, want %v", check.what, got, check.want)
		}
	}
	if !strings.Contains(string(agents[0]), `"inRegistry":false`) || !strings.Contains(string(agents[0]), `"basis":"registry"`) {
		t.Fatalf("agent = %s", agents[0])
	}
	if BasisRecorded != "recorded" || BasisRegistry != "registry" {
		t.Fatal("a basis changed spelling")
	}
}

func TestListProjectKeepsAnAttributedRowsStoredAffiliationWithRegistryBasis(t *testing.T) {
	// The Agent has since moved to p1; the attributed row still names p-old.
	r := (&affiliationRegistry{}).project("p1").window("w1", "p1").agent("a-live", "w1", ProviderClaude, nil)
	state := t.TempDir()
	attributed := recordedIn(observed(t, "a-live", "S-attributed", t0), "p-old", "w-old", "old-name")
	attributed.AffiliationBasis = BasisRegistry
	recorded := recordedIn(observed(t, "a-live", "S-recorded", t0.Add(time.Minute)), "p-old", "w-old", "old-name")
	appendAll(t, state, attributed, recorded)

	result, err := ListProject(state, &r.reg, "p-old")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Sessions) != 2 || result.Ambiguous != 0 {
		t.Fatalf("result = %+v", result)
	}
	for i, want := range []ProjectSessionAgent{
		{AgentUID: "a-live", AgentName: "old-name", WindowUID: "w-old", InRegistry: true, Basis: BasisRegistry},
		{AgentUID: "a-live", AgentName: "old-name", WindowUID: "w-old", InRegistry: true, Basis: BasisRecorded},
	} {
		if got := result.Sessions[i].Agents; len(got) != 1 || got[0] != want {
			t.Fatalf("session %d agents = %+v, want %+v", i, got, want)
		}
	}
}
