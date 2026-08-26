package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

func testActivationRegistryPath(t *testing.T) string {
	t.Helper()
	return intmetadata.PathFor(t.TempDir())
}

// TestSuperviseWaitsForTheCommittedActivationBeforeStartingTheChild is the
// deterministic form of the L17 first-attempt failure. A create holds the
// Registry transaction while tmux starts this supervisor. The provider must
// not run until that transaction has committed the exact Pane generation;
// otherwise an immediate exit can fire pane-exited between UID claim and
// commit, leaving rollback to observe the original "ownership uid is empty"
// signature.
func TestSuperviseWaitsForTheCommittedActivationBeforeStartingTheChild(t *testing.T) {
	registryPath := testActivationRegistryPath(t)
	store := newFakeResourceStore(t)
	activatePaneFixture(t, store, "pan-alpha-codex", "agt-alpha-codex", "gen-early-exit")
	pane, _ := store.registry.Pane("pan-alpha-codex")
	pane.Status.Activation.RuntimeID = "%9"

	gateEntered := make(chan struct{})
	releaseCommit := make(chan struct{})
	locked := store.store()
	locked.updateConvergent = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error) {
		close(gateEntered)
		<-releaseCommit
		working := store.registry.Clone()
		if err := fn(&working); err != nil {
			return coremetadata.Registry{}, false, err
		}
		return working, false, nil
	}

	childStarted := make(chan struct{})
	done := make(chan error, 1)
	cmd := &activationExecCommand{
		store: locked,
		lookupEnv: func(key string) (string, bool) {
			if key == "TMUX_PANE" {
				return "%9", true
			}
			return "", false
		},
		exec: func([]string, string, superviseSpec) error {
			close(childStarted)
			return nil
		},
	}
	go func() {
		done <- cmd.Run([]string{
			"--pane-uid", "pan-alpha-codex",
			"--agent-uid", "agt-alpha-codex",
			"--generation", "gen-early-exit",
			"--operation-id", "op-test",
			"--registry-path", registryPath,
			"--", "sh", "-c", "exit 42",
		}, nil, nil)
	}()

	select {
	case <-childStarted:
		t.Fatal("provider started before the create transaction released its committed activation")
	case <-gateEntered:
	}
	select {
	case <-childStarted:
		t.Fatal("provider crossed the still-held create transaction")
	default:
	}
	close(releaseCommit)
	select {
	case <-childStarted:
	case <-time.After(time.Second):
		t.Fatal("provider did not start after the exact activation committed")
	}
	if err := <-done; err != nil {
		t.Fatalf("provider exec after commit = %v", err)
	}
}

func TestSuperviseRefusesTheChildWhenCreateAbortsBeforeActivationCommit(t *testing.T) {
	registryPath := testActivationRegistryPath(t)
	store := newFakeResourceStore(t)
	gateEntered := make(chan struct{})
	releaseAbort := make(chan struct{})
	locked := store.store()
	locked.updateConvergent = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error) {
		close(gateEntered)
		<-releaseAbort
		working := store.registry.Clone() // the aborted create committed no activation
		if err := fn(&working); err != nil {
			return coremetadata.Registry{}, false, err
		}
		return working, false, nil
	}

	childStarted := make(chan struct{})
	done := make(chan error, 1)
	cmd := &activationExecCommand{
		store:     locked,
		lookupEnv: func(key string) (string, bool) { return "%9", key == "TMUX_PANE" },
		exec: func([]string, string, superviseSpec) error {
			close(childStarted)
			return nil
		},
	}
	go func() {
		done <- cmd.Run([]string{
			"--pane-uid", "pan-alpha-codex", "--agent-uid", "agt-alpha-codex",
			"--generation", "gen-aborted", "--operation-id", "op-aborted",
			"--registry-path", registryPath,
			"--", "provider",
		}, nil, nil)
	}()

	<-gateEntered
	close(releaseAbort)
	err := <-done
	if err == nil || !strings.Contains(err.Error(), "does not carry the exact committed activation") {
		t.Fatalf("aborted activation error = %v", err)
	}
	select {
	case <-childStarted:
		t.Fatal("an aborted create started its provider")
	default:
	}
}

// activatePaneFixture stamps one generation onto a fixture Pane so a receipt
// has something current to match.
func activatePaneFixture(t *testing.T, store *fakeResourceStore, paneUID, agentUID, generation string) {
	activatePaneFixtureForOperation(t, store, paneUID, agentUID, generation, "op-test", "")
}

func activatePaneFixtureForOperation(t *testing.T, store *fakeResourceStore, paneUID, agentUID, generation, operationID, runtimeID string) {
	t.Helper()
	if _, err := store.store().update(func(working *coremetadata.Registry) error {
		_, err := store.mutator().RecordPaneActivation(working, paneUID, coremetadata.PaneActivationOptions{
			Generation: generation, AgentUID: agentUID, OperationID: operationID,
		})
		if err == nil {
			pane, _ := working.Pane(paneUID)
			pane.Status.Activation.RuntimeID = runtimeID
		}
		return err
	}); err != nil {
		t.Fatalf("activate %s: %v", paneUID, err)
	}
}

func newTestSuperviseCommand(t *testing.T, store *fakeResourceStore, outcome processOutcome, runErr error) (*superviseCommand, *[]string) {
	t.Helper()
	var started []string
	cmd := &superviseCommand{
		store:   store.store(),
		journal: terminationJournal{path: filepath.Join(t.TempDir(), terminationJournalFile)},
		now:     func() time.Time { return resourceFixtureClock },
		run: func(argv []string, argv0 string) (processOutcome, error) {
			started = append(started, strings.Join(append([]string{argv0}, argv...), " "))
			return outcome, runErr
		},
	}
	return cmd, &started
}

func readTestTerminationJournal(t *testing.T, cmd *superviseCommand) []coremetadata.TerminationEvidence {
	t.Helper()
	receipts, err := cmd.journal.read()
	if err != nil {
		t.Fatalf("read termination journal: %v", err)
	}
	return receipts
}

// TestSuperviseArgvTerminatesItsOwnFlags proves an operator payload can never
// be re-read as a supervisor flag.
func TestSuperviseArgvTerminatesItsOwnFlags(t *testing.T) {
	t.Parallel()

	spec := superviseSpec{PaneUID: "pan-alpha-log", Generation: "gen-1", OperationID: "op-1"}
	argv := superviseArgv("/opt/projmux/bin/projmux", spec, "-zsh",
		[]string{"claude", "--pane-uid", "spoofed", "--generation", "spoofed"})
	want := []string{
		"/opt/projmux/bin/projmux", "internal", "supervise",
		"--pane-uid", "pan-alpha-log", "--generation", "gen-1",
		"--operation-id", "op-1", "--argv0", "-zsh",
		"--", "claude", "--pane-uid", "spoofed", "--generation", "spoofed",
	}
	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv = %v, want %v", argv, want)
	}

	// The route parses exactly what the builder wrote, not the payload's
	// lookalike flags.
	store := newFakeResourceStore(t)
	activatePaneFixtureForOperation(t, store, "pan-alpha-log", "", "gen-1", "op-1", "%7")
	cmd, started := newTestSuperviseCommand(t, store, processOutcome{ExitCode: 0}, nil)
	err := cmd.Run(argv[3:], nil, nil)
	var coded superviseExitError
	if !errors.As(err, &coded) || coded.ExitCode() != 0 {
		t.Fatalf("supervise exit = %v", err)
	}
	if len(*started) != 1 || !strings.HasSuffix((*started)[0], "claude --pane-uid spoofed --generation spoofed") {
		t.Fatalf("started child = %v", *started)
	}
}

func TestSuperviseActivationAdmissionIsExactAndZeroWrite(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		paneUID    string
		agentUID   string
		generation string
		operation  string
		runtimeID  string
		mutate     func(*coremetadata.Registry)
		want       string
	}{
		{name: "missing Pane", paneUID: "pan-missing", agentUID: "agt-alpha-codex", generation: "gen-exact", operation: "op-exact", runtimeID: "%9", want: "is not committed"},
		{name: "generation mismatch", paneUID: "pan-alpha-codex", agentUID: "agt-alpha-codex", generation: "gen-foreign", operation: "op-exact", runtimeID: "%9", want: "exact committed activation"},
		{name: "operation mismatch", paneUID: "pan-alpha-codex", agentUID: "agt-alpha-codex", generation: "gen-exact", operation: "op-foreign", runtimeID: "%9", want: "exact committed activation"},
		{name: "runtime mismatch", paneUID: "pan-alpha-codex", agentUID: "agt-alpha-codex", generation: "gen-exact", operation: "op-exact", runtimeID: "%77", want: "exact committed activation"},
		{name: "Agent capability mismatch", paneUID: "pan-alpha-codex", agentUID: "agt-beta-codex", generation: "gen-exact", operation: "op-exact", runtimeID: "%9", want: "exact committed activation"},
		{
			name: "foreign Pane owner", paneUID: "pan-alpha-codex", agentUID: "agt-alpha-codex", generation: "gen-exact", operation: "op-exact", runtimeID: "%9",
			mutate: func(registry *coremetadata.Registry) {
				pane, _ := registry.Pane("pan-alpha-codex")
				pane.Metadata.OwnerRef.UID = "agt-foreign"
			}, want: "is not owned by agent",
		},
		{
			name: "non-Agent Pane role", paneUID: "pan-alpha-codex", agentUID: "agt-alpha-codex", generation: "gen-exact", operation: "op-exact", runtimeID: "%9",
			mutate: func(registry *coremetadata.Registry) {
				pane, _ := registry.Pane("pan-alpha-codex")
				pane.Spec.Role = coremetadata.PaneRoleShell
			}, want: "is not owned by agent",
		},
		{
			name: "missing Agent", paneUID: "pan-alpha-codex", agentUID: "agt-alpha-codex", generation: "gen-exact", operation: "op-exact", runtimeID: "%9",
			mutate: func(registry *coremetadata.Registry) {
				registry.Agents = slices.DeleteFunc(registry.Agents, func(agent coremetadata.Agent) bool { return agent.Metadata.UID == "agt-alpha-codex" })
			}, want: "does not carry the exact Running pane binding",
		},
		{
			name: "Agent is no longer Running", paneUID: "pan-alpha-codex", agentUID: "agt-alpha-codex", generation: "gen-exact", operation: "op-exact", runtimeID: "%9",
			mutate: func(registry *coremetadata.Registry) {
				agent, _ := registry.Agent("agt-alpha-codex")
				agent.Status.Phase = coremetadata.PhaseOffline
			}, want: "does not carry the exact Running pane binding",
		},
		{
			name: "Agent points at a sibling Pane", paneUID: "pan-alpha-codex", agentUID: "agt-alpha-codex", generation: "gen-exact", operation: "op-exact", runtimeID: "%9",
			mutate: func(registry *coremetadata.Registry) {
				agent, _ := registry.Agent("agt-alpha-codex")
				agent.Status.PaneRef = "pan-alpha-log"
			}, want: "does not carry the exact Running pane binding",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			registryPath := testActivationRegistryPath(t)
			store := newFakeResourceStore(t)
			activatePaneFixtureForOperation(t, store, "pan-alpha-codex", "agt-alpha-codex", "gen-exact", "op-exact", "%9")
			if test.mutate != nil {
				test.mutate(&store.registry)
			}
			before, err := json.Marshal(store.registry)
			if err != nil {
				t.Fatal(err)
			}
			transactions, writes := store.transactions, store.writes
			started := false
			cmd := &activationExecCommand{
				store:     store.store(),
				lookupEnv: func(key string) (string, bool) { return test.runtimeID, key == "TMUX_PANE" },
				exec: func([]string, string, superviseSpec) error {
					started = true
					return nil
				},
			}
			err = cmd.Run([]string{
				"--pane-uid", test.paneUID, "--agent-uid", test.agentUID,
				"--generation", test.generation, "--operation-id", test.operation,
				"--registry-path", registryPath,
				"--", "sh", "-c", "exit 0",
			}, nil, nil)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("admission error = %v, want %q", err, test.want)
			}
			if started {
				t.Fatal("a refused activation started the provider")
			}
			if store.transactions != transactions+1 || store.writes != writes {
				t.Fatalf("admission transactions/writes = %d/%d, want %d/%d", store.transactions, store.writes, transactions+1, writes)
			}
			after, marshalErr := json.Marshal(store.registry)
			if marshalErr != nil || string(after) != string(before) {
				t.Fatalf("refusal changed Registry bytes: err=%v\nbefore=%s\nafter=%s", marshalErr, before, after)
			}
		})
	}

	t.Run("exact committed binding executes once and writes zero Registry bytes", func(t *testing.T) {
		t.Parallel()
		registryPath := testActivationRegistryPath(t)
		store := newFakeResourceStore(t)
		activatePaneFixtureForOperation(t, store, "pan-alpha-codex", "agt-alpha-codex", "gen-exact", "op-exact", "%9")
		before, err := json.Marshal(store.registry)
		if err != nil {
			t.Fatal(err)
		}
		transactions, writes := store.transactions, store.writes
		var gotArgv []string
		var gotArgv0 string
		var gotSpec superviseSpec
		cmd := &activationExecCommand{
			store: store.store(), lookupEnv: func(key string) (string, bool) { return "%9", key == "TMUX_PANE" },
			exec: func(argv []string, argv0 string, spec superviseSpec) error {
				gotArgv, gotArgv0, gotSpec = append([]string(nil), argv...), argv0, spec
				return nil
			},
		}
		err = cmd.Run([]string{
			"--pane-uid", "pan-alpha-codex", "--agent-uid", "agt-alpha-codex",
			"--generation", "gen-exact", "--operation-id", "op-exact", "--argv0", "-provider",
			"--registry-path", registryPath,
			"--", "provider", "--exact-arg",
		}, nil, nil)
		if err != nil {
			t.Fatalf("exact activation exec = %v", err)
		}
		if strings.Join(gotArgv, "\x00") != "provider\x00--exact-arg" || gotArgv0 != "-provider" ||
			gotSpec != (superviseSpec{PaneUID: "pan-alpha-codex", AgentUID: "agt-alpha-codex", Generation: "gen-exact", OperationID: "op-exact", RuntimeID: "%9", RegistryPath: registryPath}) {
			t.Fatalf("exec handoff = argv=%v argv0=%q spec=%+v", gotArgv, gotArgv0, gotSpec)
		}
		if store.transactions != transactions+1 || store.writes != writes {
			t.Fatalf("exact admission transactions/writes = %d/%d, want %d/%d", store.transactions, store.writes, transactions+1, writes)
		}
		after, marshalErr := json.Marshal(store.registry)
		if marshalErr != nil || string(after) != string(before) {
			t.Fatalf("exact admission changed Registry bytes: err=%v\nbefore=%s\nafter=%s", marshalErr, before, after)
		}
	})

	t.Run("blank inherited runtime refuses before entering the Registry", func(t *testing.T) {
		t.Parallel()
		registryPath := testActivationRegistryPath(t)
		store := newFakeResourceStore(t)
		started := false
		cmd := &activationExecCommand{
			store:     store.store(),
			lookupEnv: func(string) (string, bool) { return "", false },
			exec:      func([]string, string, superviseSpec) error { started = true; return nil },
		}
		err := cmd.Run([]string{"--pane-uid", "pan-alpha-codex", "--agent-uid", "agt-alpha-codex", "--generation", "gen-exact", "--operation-id", "op-exact", "--registry-path", registryPath, "--", "false"}, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "TMUX_PANE is empty") || started || store.transactions != 0 || store.writes != 0 {
			t.Fatalf("blank runtime refusal = err=%v started=%t transactions=%d writes=%d", err, started, store.transactions, store.writes)
		}
	})

	t.Run("malformed inherited runtime refuses before entering the Registry", func(t *testing.T) {
		t.Parallel()
		registryPath := testActivationRegistryPath(t)
		store := newFakeResourceStore(t)
		started := false
		cmd := &activationExecCommand{
			store:     store.store(),
			lookupEnv: func(string) (string, bool) { return "pane-9", true },
			exec:      func([]string, string, superviseSpec) error { started = true; return nil },
		}
		err := cmd.Run([]string{"--pane-uid", "pan-alpha-codex", "--agent-uid", "agt-alpha-codex", "--generation", "gen-exact", "--operation-id", "op-exact", "--registry-path", registryPath, "--", "false"}, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "is not an exact tmux Pane handle") || started || store.transactions != 0 || store.writes != 0 {
			t.Fatalf("malformed runtime refusal = err=%v started=%t transactions=%d writes=%d", err, started, store.transactions, store.writes)
		}
	})
}

func TestActivationAuthorityRejectsNonExactRegistryPathsBeforeProviderStart(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		path string
		want string
	}{
		{name: "blank", want: "absolute Registry path"},
		{name: "relative", path: filepath.Join("relative", "projmux", "metadata", "registry.json"), want: "absolute Registry path"},
		{name: "non-clean", path: string(filepath.Separator) + "state" + string(filepath.Separator) + "projmux" + string(filepath.Separator) + "metadata" + string(filepath.Separator) + ".." + string(filepath.Separator) + "metadata" + string(filepath.Separator) + "registry.json", want: "clean Registry path"},
		{name: "unexpected shape", path: filepath.Join(string(filepath.Separator), "state", "projmux", "other.json"), want: "unexpected shape"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			started := false
			cmd := &activationExecCommand{
				store:     newFakeResourceStore(t).store(),
				lookupEnv: func(string) (string, bool) { return "%9", true },
				exec:      func([]string, string, superviseSpec) error { started = true; return nil },
			}
			err := cmd.Run([]string{
				"--pane-uid", "pan-alpha-codex", "--agent-uid", "agt-alpha-codex",
				"--generation", "gen-exact", "--operation-id", "op-exact",
				"--registry-path", test.path, "--", "provider",
			}, nil, nil)
			if err == nil || !strings.Contains(err.Error(), test.want) || started {
				t.Fatalf("Registry path refusal = err=%v started=%t, want %q", err, started, test.want)
			}
		})
	}
}

func TestAgentAdmissionAndReceiptUseCreatorRegistryAuthorityNotAmbientXDG(t *testing.T) {
	root := t.TempDir()
	creatorStateDir := filepath.Join(root, "creator state", "projmux")
	creatorRegistryPath := intmetadata.PathFor(creatorStateDir)
	ambientStateHome := filepath.Join(root, "ambient-server-state")
	t.Setenv("XDG_STATE_HOME", ambientStateHome)
	t.Setenv("HOME", filepath.Join(root, "ambient-home"))

	fixture := newFakeResourceStore(t)
	activatePaneFixtureForOperation(t, fixture, "pan-alpha-codex", "agt-alpha-codex", "gen-exact", "op-exact", "%9")
	if _, err := intmetadata.NewStore(creatorRegistryPath).Update(func(registry *coremetadata.Registry) error {
		*registry = fixture.registry.Clone()
		return nil
	}); err != nil {
		t.Fatalf("write creator Registry fixture: %v", err)
	}
	before, err := os.ReadFile(creatorRegistryPath)
	if err != nil {
		t.Fatal(err)
	}

	started := 0
	gate := newActivationExecCommand()
	gate.lookupEnv = func(key string) (string, bool) { return "%9", key == "TMUX_PANE" }
	gate.exec = func([]string, string, superviseSpec) error { started++; return nil }
	if err := gate.Run([]string{
		"--pane-uid", "pan-alpha-codex", "--agent-uid", "agt-alpha-codex",
		"--generation", "gen-exact", "--operation-id", "op-exact",
		"--registry-path", creatorRegistryPath, "--", "provider",
	}, nil, nil); err != nil {
		t.Fatalf("creator-authority admission = %v", err)
	}
	if started != 1 {
		t.Fatalf("provider starts = %d, want 1", started)
	}
	after, err := os.ReadFile(creatorRegistryPath)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("admission changed creator Registry: err=%v equal=%t", err, bytes.Equal(after, before))
	}
	ambientRegistryPath := intmetadata.PathFor(filepath.Join(ambientStateHome, "projmux"))
	if _, err := os.Stat(ambientRegistryPath); !os.IsNotExist(err) {
		t.Fatalf("admission touched ambient Registry: %v", err)
	}

	supervisor := newSuperviseCommand()
	supervisor.runActivation = func([]string, string, superviseSpec) (processOutcome, error) {
		return processOutcome{ExitCode: 42}, nil
	}
	err = supervisor.Run([]string{
		"--pane-uid", "pan-alpha-codex", "--agent-uid", "agt-alpha-codex",
		"--generation", "gen-exact", "--operation-id", "op-exact",
		"--registry-path", creatorRegistryPath, "--", "provider",
	}, nil, nil)
	var coded superviseExitError
	if !errors.As(err, &coded) || coded.ExitCode() != 42 {
		t.Fatalf("supervisor exit = %v", err)
	}
	creatorJournal, err := terminationJournalForRegistryPath(creatorRegistryPath)
	if err != nil {
		t.Fatal(err)
	}
	receipts, err := creatorJournal.read()
	if err != nil || len(receipts) != 1 || receipts[0].PaneUID != "pan-alpha-codex" || receipts[0].ExitCode == nil || *receipts[0].ExitCode != 42 {
		t.Fatalf("creator journal receipts = %#v, err=%v", receipts, err)
	}
	ambientJournal := filepath.Join(ambientStateHome, "projmux", terminationJournalFile)
	if _, err := os.Stat(ambientJournal); !os.IsNotExist(err) {
		t.Fatalf("receipt touched ambient journal: %v", err)
	}
}

func TestActivationExecFailureHandshakeIsTyped(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		spec    superviseSpec
		execErr error
		wantErr bool
	}{
		{
			name: "admission refusal", wantErr: true,
			spec: superviseSpec{PaneUID: "pan-alpha-codex", AgentUID: "agt-alpha-codex", Generation: "gen-aborted", OperationID: "op-exact"},
		},
		{
			name: "provider exec failure", wantErr: true, execErr: errors.New("exec format error"),
			spec: superviseSpec{PaneUID: "pan-alpha-codex", AgentUID: "agt-alpha-codex", Generation: "gen-exact", OperationID: "op-exact"},
		},
		{
			name: "provider exec success", spec: superviseSpec{PaneUID: "pan-alpha-codex", AgentUID: "agt-alpha-codex", Generation: "gen-exact", OperationID: "op-exact"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			registryPath := testActivationRegistryPath(t)
			store := newFakeResourceStore(t)
			activatePaneFixtureForOperation(t, store, "pan-alpha-codex", "agt-alpha-codex", "gen-exact", "op-exact", "%9")
			var failure bytes.Buffer
			execCalls := 0
			cmd := &activationExecCommand{
				store: store.store(), lookupEnv: func(key string) (string, bool) { return "%9", key == "TMUX_PANE" }, failure: &failure,
				exec: func([]string, string, superviseSpec) error { execCalls++; return test.execErr },
			}
			err := cmd.Run([]string{
				"--pane-uid", test.spec.PaneUID, "--agent-uid", test.spec.AgentUID,
				"--generation", test.spec.Generation, "--operation-id", test.spec.OperationID,
				"--registry-path", registryPath,
				"--", "provider",
			}, nil, nil)
			if test.wantErr {
				if err == nil || failure.String() != "activation-failed\n" {
					t.Fatalf("failure handshake = err=%v marker=%q", err, failure.String())
				}
				if test.execErr == nil && execCalls != 0 {
					t.Fatalf("admission refusal executed provider %d times", execCalls)
				}
				return
			}
			if err != nil || failure.Len() != 0 || execCalls != 1 {
				t.Fatalf("success handshake = err=%v marker=%q execCalls=%d", err, failure.String(), execCalls)
			}
		})
	}

	argv := activationExecArgv("/opt/projmux/bin/projmux",
		superviseSpec{PaneUID: "pane-1", AgentUID: "agent-1", Generation: "gen-1", OperationID: "op-1", RegistryPath: "/state with spaces/projmux/metadata/registry.json"},
		"-provider", 3, []string{"provider", "--literal"})
	want := []string{
		"/opt/projmux/bin/projmux", "internal", "activation-exec",
		"--pane-uid", "pane-1", "--agent-uid", "agent-1",
		"--generation", "gen-1", "--operation-id", "op-1", "--registry-path", "/state with spaces/projmux/metadata/registry.json", "--failure-fd", "3",
		"--argv0", "-provider", "--", "provider", "--literal",
	}
	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("activation exec argv = %v, want %v", argv, want)
	}
}

func TestSupervisePreservesExactExitOutcome(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		outcome processOutcome
		code    int
	}{
		{name: "exit zero", outcome: processOutcome{ExitCode: 0}},
		{name: "exit 42", outcome: processOutcome{ExitCode: 42}, code: 42},
		{name: "signal", outcome: processOutcome{Signal: "TERM", SignalNumber: 15}, code: 143},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			cmd := &superviseCommand{
				store: store.store(), journal: terminationJournal{path: filepath.Join(t.TempDir(), terminationJournalFile)},
				runActivation: func([]string, string, superviseSpec) (processOutcome, error) { return test.outcome, nil },
			}
			err := cmd.Run([]string{"--pane-uid", "pan-alpha-codex", "--agent-uid", "agt-alpha-codex", "--generation", "gen-exact", "--operation-id", "op-exact", "--", "provider"}, nil, nil)
			var coded superviseExitError
			if !errors.As(err, &coded) || coded.ExitCode() != test.code {
				t.Fatalf("supervise exit = %v, want %d", err, test.code)
			}
			if store.transactions != 0 || store.writes != 0 {
				t.Fatalf("post-exit supervisor entered Registry: transactions/writes = %d/%d", store.transactions, store.writes)
			}
			receipts := readTestTerminationJournal(t, cmd)
			if len(receipts) != 1 || receipts[0].PaneUID != "pan-alpha-codex" || receipts[0].Generation != "gen-exact" || receipts[0].OperationID != "op-exact" {
				t.Fatalf("termination receipts = %#v", receipts)
			}
		})
	}

	t.Run("non-Agent Pane stays outside Agent ownership admission", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		cmd, started := newTestSuperviseCommand(t, store, processOutcome{}, nil)
		transactions := store.transactions
		err := cmd.Run([]string{"--pane-uid", "pan-alpha-log", "--generation", "gen-shell", "--operation-id", "op-shell", "--", "shell"}, nil, nil)
		var coded superviseExitError
		if !errors.As(err, &coded) || len(*started) != 1 || store.transactions != transactions {
			t.Fatalf("shell compatibility = err=%v started=%v transactions=%d, want %d", err, *started, store.transactions, transactions)
		}
	})
}

// TestSuperviseRecordsTheObservedWaitStatus is the schema half of the process
// contract: what the supervisor reaped is what the registry stores.
func TestSuperviseRecordsTheObservedWaitStatus(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		outcome  processOutcome
		want     coremetadata.TerminationClassification
		wantCode *int
		wantSig  string
		wantExit int
	}{
		{name: "clean exit", outcome: processOutcome{ExitCode: 0}, want: coremetadata.TerminationNormal, wantCode: exitCodePtr(0)},
		{name: "non-zero exit", outcome: processOutcome{ExitCode: 17}, want: coremetadata.TerminationAbnormal, wantCode: exitCodePtr(17), wantExit: 17},
		{
			name:    "signal",
			outcome: processOutcome{Signal: "TERM", SignalNumber: 15},
			want:    coremetadata.TerminationAbnormal, wantSig: "TERM", wantExit: 143,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			activatePaneFixture(t, store, "pan-alpha-log", "", "gen-exact")
			cmd, _ := newTestSuperviseCommand(t, store, test.outcome, nil)
			err := cmd.Run([]string{"--pane-uid", "pan-alpha-log", "--generation", "gen-exact", "--", "sleep", "1"}, nil, nil)
			var coded superviseExitError
			if !errors.As(err, &coded) || coded.ExitCode() != test.wantExit {
				t.Fatalf("supervise exit = %v, want status %d", err, test.wantExit)
			}
			receipts := readTestTerminationJournal(t, cmd)
			if len(receipts) != 1 {
				t.Fatalf("journal receipts = %#v, want exactly one", receipts)
			}
			receipt := &receipts[0]
			if receipt.Source != coremetadata.TerminationSourceSupervisor || receipt.Classification != test.want {
				t.Fatalf("receipt = %#v, want %q from the supervisor", receipt, test.want)
			}
			if receipt.Generation != "gen-exact" {
				t.Fatalf("receipt generation = %q, want the launched one", receipt.Generation)
			}
			switch {
			case test.wantSig != "":
				if receipt.Signal != test.wantSig || receipt.ExitCode != nil {
					t.Fatalf("signal receipt = %#v", receipt)
				}
			default:
				if receipt.ExitCode == nil || *receipt.ExitCode != *test.wantCode || receipt.Signal != "" {
					t.Fatalf("exit receipt = %#v", receipt)
				}
			}
		})
	}
}

// TestSuperviseKeepsThePaneStatusWhenTheReceiptCannotBeWritten is the
// unknown-safe fallback: a registry that cannot be written costs the evidence,
// never the pane's own behavior.
func TestSuperviseKeepsThePaneStatusWhenTheReceiptCannotBeWritten(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	activatePaneFixture(t, store, "pan-alpha-log", "", "gen-exact")
	cmd, _ := newTestSuperviseCommand(t, store, processOutcome{ExitCode: 5}, nil)
	cmd.journal.path = t.TempDir()
	var warnings strings.Builder
	cmd.warn = &warnings

	err := cmd.Run([]string{"--pane-uid", "pan-alpha-log", "--generation", "gen-exact", "--", "sleep", "1"}, nil, nil)
	var coded superviseExitError
	if !errors.As(err, &coded) || coded.ExitCode() != 5 {
		t.Fatalf("supervise exit = %v, want the child's own status", err)
	}
	if !strings.Contains(warnings.String(), "append termination receipt") {
		t.Fatalf("warnings = %q, want the lost write reported", warnings.String())
	}
	if pane, _ := store.registry.Pane("pan-alpha-log"); pane.Status.LastTermination != nil {
		t.Fatalf("a failed write stored something: %#v", pane.Status.LastTermination)
	}
}

// TestSupervisorPrewriteDefersGenerationValidationToConvergence covers a
// receipt that arrives after the binding it names is gone.
func TestSupervisorPrewriteDefersGenerationValidationToConvergence(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	activatePaneFixture(t, store, "pan-alpha-log", "", "gen-current")
	cmd, _ := newTestSuperviseCommand(t, store, processOutcome{ExitCode: 0}, nil)
	var warnings strings.Builder
	cmd.warn = &warnings

	err := cmd.Run([]string{"--pane-uid", "pan-alpha-log", "--generation", "gen-replaced", "--", "sleep", "1"}, nil, nil)
	var coded superviseExitError
	if !errors.As(err, &coded) || coded.ExitCode() != 0 {
		t.Fatalf("supervise exit = %v", err)
	}
	if warnings.Len() != 0 {
		t.Fatalf("warnings = %q, want lock-free prewrite to defer the guard", warnings.String())
	}
	if receipts := readTestTerminationJournal(t, cmd); len(receipts) != 1 || receipts[0].Generation != "gen-replaced" {
		t.Fatalf("journal receipts = %#v, want the offered generation", receipts)
	}
	if pane, _ := store.registry.Pane("pan-alpha-log"); pane.Status.LastTermination != nil {
		t.Fatalf("a stale receipt was stored: %#v", pane.Status.LastTermination)
	}
}

// TestSuperviseRefusesAnUnidentifiedLaunch keeps the route from writing a
// receipt it cannot bind to anything.
func TestSuperviseRefusesAnUnidentifiedLaunch(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	for _, args := range [][]string{
		{"--generation", "gen-1", "--", "sleep", "1"},
		{"--pane-uid", "pan-alpha-log", "--", "sleep", "1"},
		{"--pane-uid", "pan-alpha-log", "--generation", "gen-1"},
	} {
		cmd, started := newTestSuperviseCommand(t, store, processOutcome{}, nil)
		err := cmd.Run(args, nil, &strings.Builder{})
		if err == nil || !IsUsageError(err) {
			t.Fatalf("supervise %v error = %v, want a usage refusal", args, err)
		}
		if len(*started) != 0 {
			t.Fatalf("supervise %v started a child before refusing", args)
		}
	}
}

// TestSuperviseReportsALaunchFailureRatherThanATermination keeps a process that
// never ran out of the termination vocabulary.
func TestSuperviseReportsALaunchFailureRatherThanATermination(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	activatePaneFixture(t, store, "pan-alpha-log", "", "gen-exact")
	cmd, _ := newTestSuperviseCommand(t, store, processOutcome{}, errors.New("exec format error"))
	err := cmd.Run([]string{"--pane-uid", "pan-alpha-log", "--generation", "gen-exact", "--", "/nope"}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "exec format error") {
		t.Fatalf("launch failure = %v", err)
	}
	if pane, _ := store.registry.Pane("pan-alpha-log"); pane.Status.LastTermination != nil {
		t.Fatalf("a process that never ran produced a receipt: %#v", pane.Status.LastTermination)
	}
}

// TestRunSupervisedChildReportsRealWaitStatuses is the in-process half of the
// process-integration contract: a real child, really reaped.
func TestRunSupervisedChildReportsRealWaitStatuses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX wait statuses")
	}
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no POSIX shell: %v", err)
	}
	for _, test := range []struct {
		name    string
		script  string
		want    processOutcome
		wantCls coremetadata.TerminationClassification
	}{
		{name: "clean exit", script: "exit 0", want: processOutcome{ExitCode: 0}, wantCls: coremetadata.TerminationNormal},
		{name: "non-zero exit", script: "exit 9", want: processOutcome{ExitCode: 9}, wantCls: coremetadata.TerminationAbnormal},
		{
			name: "death by signal", script: "kill -TERM $$; sleep 5",
			want:    processOutcome{Signal: "TERM", SignalNumber: 15},
			wantCls: coremetadata.TerminationAbnormal,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			outcome, err := runSupervisedChild([]string{shell, "-c", test.script}, "")
			if err != nil {
				t.Fatalf("runSupervisedChild error = %v", err)
			}
			if outcome != test.want {
				t.Fatalf("outcome = %#v, want %#v", outcome, test.want)
			}
			if got := coremetadata.ClassifyProcessExit(outcome.ExitCode, outcome.Signal); got != test.wantCls {
				t.Fatalf("classification = %q, want %q", got, test.wantCls)
			}
		})
	}
}

// TestRunSupervisedChildPreservesArgvCwdAndEnvironment is the parity half: the
// child sees exactly the process it would have seen unsupervised.
func TestRunSupervisedChildPreservesArgvCwdAndEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell")
	}
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no POSIX shell: %v", err)
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "observed")
	t.Setenv("PROJMUX_SUPERVISE_PARITY", "inherited")
	// The supervisor never sets a working directory: the child inherits this
	// process's, exactly as an exec'd pane command would.
	wantDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	script := `printf '%s|%s|%s|%s\n' "$0" "$1" "$(pwd)" "$PROJMUX_SUPERVISE_PARITY" > ` + out
	outcome, err := runSupervisedChild([]string{shell, "-c", script, "argv-zero", "first"}, "")
	if err != nil || outcome.ExitCode != 0 {
		t.Fatalf("parity child outcome=%#v err=%v", outcome, err)
	}
	observed, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read observation: %v", err)
	}
	fields := strings.Split(strings.TrimSpace(string(observed)), "|")
	if len(fields) != 4 {
		t.Fatalf("observation = %q", observed)
	}
	if fields[0] != "argv-zero" || fields[1] != "first" {
		t.Fatalf("argv = %q, want it forwarded verbatim", fields[:2])
	}
	resolvedDir, err := filepath.EvalSymlinks(wantDir)
	if err != nil {
		t.Fatalf("resolve working directory: %v", err)
	}
	if observedDir, err := filepath.EvalSymlinks(fields[2]); err != nil || observedDir != resolvedDir {
		t.Fatalf("child cwd = %q, want %q", fields[2], resolvedDir)
	}
	if fields[3] != "inherited" {
		t.Fatalf("child environment = %q, want the inherited value", fields[3])
	}
}

// TestSuperviseRouteIsReachableThroughTheInternalNamespace keeps the hidden
// route wired the way a launched pane spells it.
func TestSuperviseRouteIsReachableThroughTheInternalNamespace(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	activatePaneFixture(t, store, "pan-alpha-log", "", "gen-exact")
	supervise, _ := newTestSuperviseCommand(t, store, processOutcome{ExitCode: 0}, nil)
	internal := &internalCommand{supervise: supervise}
	err := internal.Run([]string{"supervise", "--pane-uid", "pan-alpha-log", "--generation", "gen-exact", "--", "sleep", "1"}, nil, nil)
	var coded superviseExitError
	if !errors.As(err, &coded) {
		t.Fatalf("internal supervise = %v", err)
	}
	if receipts := readTestTerminationJournal(t, supervise); len(receipts) != 1 {
		t.Fatalf("the namespaced route journaled %#v, want one receipt", receipts)
	}
}

func exitCodePtr(code int) *int { return &code }
