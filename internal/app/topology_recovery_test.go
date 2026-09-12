package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/i18n"
)

func topologyJournalFixture(t *testing.T) (*diagnostics.LifecycleRecorder, *diagnosticsCommand, *diagnostics.Store) {
	t.Helper()
	state := t.TempDir()
	lookup := func(key string) string {
		if key == "XDG_STATE_HOME" {
			return state
		}
		return ""
	}
	home := func() (string, error) { return state, nil }
	path, err := diagnostics.DefaultPath(lookup, home)
	if err != nil {
		t.Fatal(err)
	}
	store := diagnostics.NewStore(path)
	return diagnostics.NewLifecycleRecorder(store, "recovery-invocation", "1.0.0", "tmux"), &diagnosticsCommand{lookupEnv: lookup, homeDir: home}, store
}

func readTopologyEvents(t *testing.T, command *diagnosticsCommand) []diagnostics.Event {
	t.Helper()
	var out bytes.Buffer
	if err := command.Run([]string{"log", "--component", "topology", "--json", "--tail", "1000"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var events []diagnostics.Event
	for line := range strings.SplitSeq(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var event diagnostics.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	return events
}

func assertTopologyEvents(t *testing.T, events []diagnostics.Event, result string, resumed, skipped int) {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("no topology outcome")
	}
	e := events[0]
	if e.Event != "topology.outcome" || e.Result != result || e.ResumedCount == nil || e.SkippedCount == nil || *e.ResumedCount != resumed || *e.SkippedCount != skipped {
		t.Fatalf("outcome=%+v want %s %d/%d", e, result, resumed, skipped)
	}
	total := 0
	seen := map[string]bool{}
	for _, event := range events[1:] {
		if event.Event != "topology.agent.skipped" || event.RunID != e.RunID || event.ItemCount == nil || seen[event.Code] {
			t.Fatalf("duplicate/unrelated reason=%+v", event)
		}
		seen[event.Code] = true
		total += *event.ItemCount
	}
	if total != skipped {
		t.Fatalf("reasons=%d skipped=%d", total, skipped)
	}
}

func TestTopologyRecoveryStartupCommittedSummaryAndJournal(t *testing.T) {
	for _, locale := range []i18n.Locale{i18n.FallbackLocale, i18n.Locale("ko-KR")} {
		for _, n := range []int{0, 1, 9} {
			t.Run(fmt.Sprintf("%s/%d", locale, n), func(t *testing.T) {
				t.Setenv("HOME", t.TempDir())
				t.Setenv("XDG_CONFIG_HOME", t.TempDir())
				t.Setenv("LANG", string(locale))
				activation, store, _, root, _ := newProjectStartupTopologyFixture(t)
				recorder, cli, _ := topologyJournalFixture(t)
				activation.diagnostics = recorder
				runner := &recordingNoticeRunner{}
				var stderr bytes.Buffer
				sink := &projectStartupNoticeSink{runner: runner, mirror: &stderr, lookupEnv: func(string) string { return "/isolated/client" }}
				activation.notices = sink
				for i := range n {
					for _, missing := range []bool{false, true} {
						name := fmt.Sprintf("%s-%d-%t", strings.Repeat("긴한글이름", 7), i, missing)
						ref := codexConversationRef(fmt.Sprintf("private-conversation-%d", i))
						if missing {
							ref = nil
						}
						agent := addTopologyFixtureAgent(t, store, topologyFixtureAgent{name: name, provider: "codex", cwd: root, ref: ref, topic: "private-prompt-auth"})
						markTopologyAgentInterrupted(t, store, agent.Metadata.UID, "")
					}
				}
				ok, err := activation.MaterializeProjectTopology(context.Background(), projectTopologyMaterializeRequest{Root: root, SessionName: "beta"})
				if err != nil || !ok {
					t.Fatalf("materialize=%t err=%v stderr=%s", ok, err, stderr.String())
				}
				assertTopologyEvents(t, readTopologyEvents(t, cli), "success", n, n)
				if len(runner.calls) != 1 {
					t.Fatalf("display calls=%v", runner.calls)
				}
				message := runner.calls[0][2]
				want := topologyRecoverySummary(locale, diagnostics.LifecycleSuccess, diagnostics.TopologyCounts{Resumed: n, Skipped: n})
				if message != want || len(message) > 220 || !utf8.ValidString(message) || !strings.Contains(message, "projmux diagnostics log --component topology") {
					t.Fatalf("summary %d bytes=%q want %q", len(message), message, want)
				}
				if n > 0 && strings.Count(stderr.String(), "was not restored") != n {
					t.Fatalf("lost detail: %s", stderr.String())
				}
				var human bytes.Buffer
				if err := cli.Run([]string{"log", "--component", "topology"}, &human, io.Discard); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(human.String(), fmt.Sprintf("resumed_count=%d skipped_count=%d", n, n)) {
					t.Fatalf("human log=%s", human.String())
				}
				for _, private := range []string{"private-conversation", "private-prompt-auth", "긴한글이름", "agt-test", "pane-test", root} {
					if strings.Contains(human.String(), private) {
						t.Fatalf("journal leaked %q", private)
					}
				}
				data, entry, err := cli.supportTopologyRecovery()
				if err != nil || entry.Status != "included" || !bytes.Contains(data, []byte("topology.outcome")) || bytes.Contains(data, []byte("recovery-invocation")) {
					t.Fatalf("support entry=%+v data=%s err=%v", entry, data, err)
				}
			})
		}
	}
}

func TestTopologyRecoveryPublicPreviewRepeatAndLockReplan(t *testing.T) {
	command, store, _, _, root, _ := newTopologyMaterializeFixture(t)
	recorder, cli, journal := topologyJournalFixture(t)
	command.diagnostics = recorder
	agent := addTopologyFixtureAgent(t, store, topologyFixtureAgent{name: "resumed", provider: "codex", cwd: root, ref: codexConversationRef("private-thread")})
	markTopologyAgentInterrupted(t, store, agent.Metadata.UID, "")
	args := []string{"resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json"}
	if _, _, err := runReconcile(t, command, append(args, "--dry-run")...); err != nil {
		t.Fatal(err)
	}
	if result, err := journal.ReadOnly(); err != nil || len(result.Events) != 0 {
		t.Fatalf("preview events=%+v err=%v", result, err)
	}
	out, _, err := runReconcile(t, command, args...)
	if err != nil || !strings.Contains(out, `"resumed": 1`) {
		t.Fatalf("out=%s err=%v", out, err)
	}
	assertTopologyEvents(t, readTopologyEvents(t, cli), "success", 1, 0)
	before := len(readTopologyEvents(t, cli))
	out, _, err = runReconcile(t, command, args...)
	if err != nil || !strings.Contains(out, `"outcome": "no-op"`) {
		t.Fatalf("repeat=%s err=%v", out, err)
	}
	assertTopologyEvents(t, readTopologyEvents(t, cli)[before:], "success", 0, 0)
	if len(command.agents.(*fakeTopologyAgentLauncher).binds) != 1 {
		t.Fatal("repeat resumed live Agent")
	}

	raced, racedStore, _, _, racedRoot, _ := newTopologyMaterializeFixture(t)
	raced.diagnostics, cli, _ = topologyJournalFixture(t)
	a := addTopologyFixtureAgent(t, racedStore, topologyFixtureAgent{name: "raced", provider: "codex", cwd: racedRoot, ref: codexConversationRef("race-thread")})
	markTopologyAgentInterrupted(t, racedStore, a.Metadata.UID, "")
	original := raced.resources.updateConvergent
	raced.resources.updateConvergent = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error) {
		current, _ := racedStore.registry.Agent(a.Metadata.UID)
		current.Status.SessionRef = nil
		return original(fn)
	}
	if _, _, err := runReconcile(t, raced, args...); err != nil {
		t.Fatal(err)
	}
	assertTopologyEvents(t, readTopologyEvents(t, cli), "success", 0, 1)
	if len(raced.agents.(*fakeTopologyAgentLauncher).binds) != 0 {
		t.Fatal("unlocked preview launch survived replan")
	}
}

func TestTopologyRecoveryFailureNeverRecordsPlannedSuccess(t *testing.T) {
	for _, failure := range []string{"route", "runtime", "bind", "commit", "anchor"} {
		t.Run(failure, func(t *testing.T) {
			command, store, server, _, root, _ := newTopologyMaterializeFixture(t)
			recorder, cli, _ := topologyJournalFixture(t)
			command.diagnostics = recorder
			agent := addTopologyFixtureAgent(t, store, topologyFixtureAgent{name: "private-agent", provider: "codex", cwd: root, ref: codexConversationRef("private-thread")})
			markTopologyAgentInterrupted(t, store, agent.Metadata.UID, "")
			missing := addTopologyFixtureAgent(t, store, topologyFixtureAgent{name: "private-missing", provider: "codex", cwd: root})
			markTopologyAgentInterrupted(t, store, missing.Metadata.UID, "")
			switch failure {
			case "route":
				server.fail = []string{"display-message"}
			case "runtime":
				server.fail = []string{"split-window"}
			case "bind":
				command.agents = &productionBindingTopologyAgentLauncher{fakeTopologyAgentLauncher: command.agents.(*fakeTopologyAgentLauncher), binder: testAICommand(t.TempDir())}
				server.fail = []string{"set-option", aiPaneAgentOption}
			case "commit":
				command.resources.updateConvergent = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error) {
					working := store.registry.Clone()
					if err := fn(&working); err != nil {
						return store.registry, false, err
					}
					return store.registry, false, errors.New("private-auth commit failure")
				}
			case "anchor":
				window, _ := store.registry.Window("win-beta-main")
				window.Spec.AnchorPaneRef = "missing-anchor"
			}
			before := store.snapshot()
			_, stderr, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
			if err == nil || !strings.Contains(stderr, "Continue failed:") || strings.Contains(stderr, "Continue: resumed") {
				t.Fatalf("err=%v stderr=%s", err, stderr)
			}
			assertTopologyEvents(t, readTopologyEvents(t, cli), "error", 0, 0)
			if store.snapshot() != before {
				t.Fatal("failure committed Registry")
			}
		})
	}
}

type topologyBrokenJournal struct{ calls int }

func (w *topologyBrokenJournal) Append(diagnostics.Event) error {
	w.calls++
	return errors.New("private writer failure")
}

type topologyBrokenDisplay struct{ calls int }

func (w *topologyBrokenDisplay) Run(context.Context, string, ...string) ([]byte, error) {
	w.calls++
	return nil, errors.New("private display failure")
}

func TestTopologyRecoveryWriterDisplayFailureAndNoClient(t *testing.T) {
	for _, client := range []bool{false, true} {
		activation, store, _, root, _ := newProjectStartupTopologyFixture(t)
		writer, display := &topologyBrokenJournal{}, &topologyBrokenDisplay{}
		activation.diagnostics = diagnostics.NewLifecycleRecorder(writer, "run", "1.0.0", "tmux")
		activation.notices = &projectStartupNoticeSink{runner: display, mirror: failingWriter{}, lookupEnv: func(string) string {
			if client {
				return "/test/client"
			}
			return ""
		}}
		agent := addTopologyFixtureAgent(t, store, topologyFixtureAgent{name: "unavailable-ref", provider: "codex", cwd: root})
		markTopologyAgentInterrupted(t, store, agent.Metadata.UID, "")
		if ok, err := activation.MaterializeProjectTopology(context.Background(), projectTopologyMaterializeRequest{Root: root, SessionName: "beta"}); err != nil || !ok {
			t.Fatalf("diagnostics changed success: %t %v", ok, err)
		}
		want := 0
		if client {
			want = 1
		}
		if writer.calls != 2 || display.calls != want {
			t.Fatalf("writer=%d display=%d", writer.calls, display.calls)
		}
	}
}

func TestTopologyRecoverySnapshotRetainsSeparateAuthority(t *testing.T) {
	activation, store, _, root, _ := newProjectStartupTopologyFixture(t)
	recorder, _, journal := topologyJournalFixture(t)
	activation.diagnostics = recorder
	agent := addTopologyFixtureAgent(t, store, topologyFixtureAgent{name: "fresh-recipe", provider: "codex", cwd: root})
	markTopologyAgentInterrupted(t, store, agent.Metadata.UID, "")
	if ok, err := activation.MaterializeProjectTopology(context.Background(), projectTopologyMaterializeRequest{Root: root, SessionName: "beta", AgentReplayAuthority: topologyAgentReplaySnapshot}); err != nil || !ok {
		t.Fatalf("snapshot=%t %v", ok, err)
	}
	if len(activation.agents.(*fakeTopologyAgentLauncher).launches) != 1 {
		t.Fatal("snapshot lost fresh fallback")
	}
	if result, err := journal.ReadOnly(); err != nil || len(result.Events) != 0 {
		t.Fatalf("snapshot wrote topology diagnostics: %+v %v", result, err)
	}
}

func TestTopologyRecoveryReportArchiveIncludesSuccess(t *testing.T) {
	command, _, _ := testReportCommand(t)
	path, err := diagnostics.DefaultPath(command.lookupEnv, command.homeDir)
	if err != nil {
		t.Fatal(err)
	}
	store := diagnostics.NewStore(path)
	recorder := diagnostics.NewLifecycleRecorder(store, "private-run", "1.0.0", "tmux")
	// Exercise a real committed producer before constructing the public report.
	activation, _, _, root, _ := newProjectStartupTopologyFixture(t)
	activation.diagnostics = recorder
	if _, err := activation.MaterializeProjectTopology(context.Background(), projectTopologyMaterializeRequest{Root: root, SessionName: "beta"}); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "report.tar.gz")
	if err := command.Run([]string{"report", "--output", destination}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	entries, all := readSupportArchive(t, destination)
	if !bytes.Contains(entries["topology-recovery.json"], []byte(`"resumed_count": 0`)) || bytes.Contains(all, []byte("private-run")) {
		t.Fatalf("recovery archive missing counts or leaked identity: %s", all)
	}
	data, entry, err := command.supportTopologyRecovery()
	if err != nil || entry.RecordCount != 1 || !bytes.Contains(data, []byte(`"resumed_count": 0`)) {
		t.Fatalf("entry=%+v data=%s err=%v", entry, data, err)
	}
}

// Each expected code names a real producer decision, including malformed
// retained evidence that cannot be committed as a valid Registry. No assertion
// infers a classification from the accompanying operator prose.
func TestTopologyRecoveryTypedReasonDecisionSites(t *testing.T) {
	for _, test := range []struct {
		name   string
		code   diagnostics.TopologyAgentReason
		change func(*coremetadata.Agent, *coremetadata.Pane, *fakeTopologyAgentLauncher)
	}{
		{"normal termination", diagnostics.TopologyAgentTerminationExcluded, func(a *coremetadata.Agent, p *coremetadata.Pane, _ *fakeTopologyAgentLauncher) {
			a.Status.LastTermination.Source = coremetadata.TerminationSourceSupervisor
			a.Status.LastTermination.Classification = coremetadata.TerminationNormal
			p.Status.LastTermination = a.Status.LastTermination.Clone()
		}},
		{"phase", diagnostics.TopologyAgentPhaseIneligible, func(a *coremetadata.Agent, _ *coremetadata.Pane, _ *fakeTopologyAgentLauncher) {
			a.Status.Phase = coremetadata.PhasePending
		}},
		{"activation", diagnostics.TopologyAgentActivationUnproven, func(_ *coremetadata.Agent, p *coremetadata.Pane, _ *fakeTopologyAgentLauncher) {
			p.Status.Activation.Generation = "new-generation"
		}},
		{"receipt", diagnostics.TopologyAgentTerminationInvalid, func(a *coremetadata.Agent, _ *coremetadata.Pane, _ *fakeTopologyAgentLauncher) {
			a.Status.LastTermination.Source = coremetadata.TerminationSourceReconcile
		}},
		{"missing ref", diagnostics.TopologyAgentSessionRefMissing, func(a *coremetadata.Agent, _ *coremetadata.Pane, _ *fakeTopologyAgentLauncher) {
			a.Status.SessionRef = nil
		}},
		{"missing discriminator", diagnostics.TopologyAgentSessionRefInvalid, func(a *coremetadata.Agent, _ *coremetadata.Pane, _ *fakeTopologyAgentLauncher) {
			a.Status.SessionRef.Provider = ""
		}},
		{"malformed union", diagnostics.TopologyAgentSessionRefInvalid, func(a *coremetadata.Agent, _ *coremetadata.Pane, _ *fakeTopologyAgentLauncher) {
			a.Status.SessionRef.Claude = &coremetadata.ClaudeSessionRef{SessionID: "private"}
		}},
		{"mismatch", diagnostics.TopologyAgentSessionRefMismatch, func(a *coremetadata.Agent, _ *coremetadata.Pane, _ *fakeTopologyAgentLauncher) {
			a.Spec.Provider = "claude"
		}},
		{"provider", diagnostics.TopologyAgentProviderUnavailable, func(_ *coremetadata.Agent, _ *coremetadata.Pane, l *fakeTopologyAgentLauncher) {
			l.disabled["codex"] = true
		}},
		{"workspace", diagnostics.TopologyAgentWorkspaceUnavailable, func(a *coremetadata.Agent, _ *coremetadata.Pane, _ *fakeTopologyAgentLauncher) {
			a.Spec.Workspace.CWD += "/missing"
		}},
		{"prepare", diagnostics.TopologyAgentResumePrepareFailed, func(_ *coremetadata.Agent, _ *coremetadata.Pane, l *fakeTopologyAgentLauncher) {
			l.resumeErr["codex"] = errors.New("termination-excluded private token prompt")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			command, store, _, _, root, _ := newTopologyMaterializeFixture(t)
			a := addTopologyFixtureAgent(t, store, topologyFixtureAgent{name: "decision", provider: "codex", cwd: root, ref: codexConversationRef("private")})
			p := markTopologyAgentInterrupted(t, store, a.Metadata.UID, "")
			agent, _ := store.registry.Agent(a.Metadata.UID)
			pane, _ := store.registry.Pane(p.Metadata.UID)
			launcher := command.agents.(*fakeTopologyAgentLauncher)
			test.change(agent, pane, launcher)
			project, _ := store.registry.Project("prj-beta")
			window, _ := store.registry.Window("win-beta-main")
			plan := &registryTopologyPlan{}
			work := planTopologyWindowAgents(plan, store.registry, *project, *window, 1, nil, launcher, "", topologyAgentReplayInterrupted)
			if len(work) != 0 || len(plan.agentSkips) != 1 || plan.agentSkips[test.code] != 1 || len(plan.notices) != 1 {
				t.Fatalf("work=%v reasons=%v notices=%v", work, plan.agentSkips, plan.notices)
			}
		})
	}
}

func TestTopologyRecoveryMixedReasonsReachJournal(t *testing.T) {
	command, store, _, _, root, _ := newTopologyMaterializeFixture(t)
	recorder, cli, _ := topologyJournalFixture(t)
	command.diagnostics = recorder
	for i, classification := range []coremetadata.TerminationClassification{coremetadata.TerminationNormal, coremetadata.TerminationInterrupted, coremetadata.TerminationAbnormal} {
		ref := codexConversationRef("private-thread")
		if i == 1 {
			ref = nil
		}
		agent := addTopologyFixtureAgent(t, store, topologyFixtureAgent{name: fmt.Sprintf("mixed-%d", i), provider: "codex", cwd: root, ref: ref})
		markTopologyAgentTermination(t, store, agent.Metadata.UID, "", classification)
	}
	if _, stderr, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json"); err != nil {
		t.Fatalf("err=%v stderr=%s", err, stderr)
	}
	events := readTopologyEvents(t, cli)
	assertTopologyEvents(t, events, "success", 1, 2)
	if len(events) != 3 || events[1].Code != string(diagnostics.TopologyAgentTerminationExcluded) || events[2].Code != string(diagnostics.TopologyAgentSessionRefMissing) {
		t.Fatalf("reasons=%+v", events)
	}
}
