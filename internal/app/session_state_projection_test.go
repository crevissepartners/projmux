package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	coresessionstate "github.com/crevissepartners/projmux/internal/core/sessionstate"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
)

type projectionMissingSessionRunner struct{ calls [][]string }

func (r *projectionMissingSessionRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	command := args
	if len(command) >= 2 && (command[0] == "-L" || command[0] == "-S") {
		command = command[2:]
	}
	if len(command) > 0 && command[0] == "has-session" {
		return nil, exec.Command("sh", "-c", "exit 1").Run()
	}
	return nil, nil
}

func projectionRunnerWriteCalls(calls [][]string) [][]string {
	var writes [][]string
	for _, call := range calls {
		command := call[1:]
		if len(command) >= 2 && (command[0] == "-L" || command[0] == "-S") {
			command = command[2:]
		}
		if len(command) == 0 || command[0] == "list-sessions" || command[0] == "has-session" {
			continue
		}
		writes = append(writes, call)
	}
	return writes
}

type projectionTrustAuthorizer struct {
	allowed     bool
	err         error
	calls       []string
	onAuthorize func()
}

func (a *projectionTrustAuthorizer) AuthorizeProjectHooks(_ context.Context, cwd string) (bool, error) {
	a.calls = append(a.calls, cwd)
	if a.onAuthorize != nil {
		a.onAuthorize()
	}
	if a.err != nil {
		return false, a.err
	}
	return a.allowed, nil
}

func projectionSnapshot(t *testing.T, registry coremetadata.Registry) coresessionstate.Snapshot {
	t.Helper()
	project, _ := registry.Project("prj-beta")
	window := registry.WindowsOf(project.Metadata.UID)[0]
	pane := registry.PanesOf(window.Metadata.UID)[0]
	return coresessionstate.Snapshot{Version: coresessionstate.Version, Session: "saved-beta", DefaultCWD: project.Spec.Root, SavedAt: resourceFixtureClock,
		Metadata: &coresessionstate.ResourceMetadata{UID: project.Metadata.UID, Name: project.Metadata.Name}, Windows: []coresessionstate.Window{{Index: 0, Name: window.Metadata.Name, ActivePaneIndex: 0, Metadata: &coresessionstate.ResourceMetadata{UID: window.Metadata.UID, Name: window.Metadata.Name, OwnerKind: string(coremetadata.KindProject), OwnerUID: project.Metadata.UID}, Panes: []coresessionstate.Pane{{Index: 0, CWD: "/srv/beta/restored", Metadata: &coresessionstate.ResourceMetadata{UID: pane.Metadata.UID, Name: pane.Metadata.Name, OwnerKind: string(coremetadata.KindWindow), OwnerUID: window.Metadata.UID}, Recipe: coresessionstate.ShellRecipe()}}}}}
}

func TestRestoreSnapshotDryRunPlansExactProjectWithZeroWrites(t *testing.T) {
	resources := newFakeResourceStore(t)
	snapshots := sessionstate.NewStore(t.TempDir())
	snap := projectionSnapshot(t, resources.registry)
	if err := snapshots.Save(snap); err != nil {
		t.Fatal(err)
	}
	cmd := &sessionStateCommand{resources: resources.store(), sessionStore: func() (sessionstate.Store, error) { return snapshots, nil }, now: func() time.Time { return resourceFixtureClock.Add(time.Hour) }, runner: &projectionMissingSessionRunner{}}
	var stdout bytes.Buffer
	if err := cmd.runRestore([]string{"--session", "saved-beta", "-p", "uid:prj-beta", "--dry-run"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if resources.writes != 0 || resources.transactions != 0 {
		t.Fatalf("dry-run writes=%d transactions=%d", resources.writes, resources.transactions)
	}
	for _, want := range []string{"replace Window", "delete Window", "preserve uid", "lose conversation pointer", "trust Project open gate pending", "snapshot startup command execution 0", "Registry writes 0 / tmux writes 0 / snapshot writes 0"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("preview %q missing %q", stdout.String(), want)
		}
	}
}

func TestRestoreSnapshotKoreanLocaleRendersProjectionResult(t *testing.T) {
	resources := newFakeResourceStore(t)
	snapshots := sessionstate.NewStore(t.TempDir())
	if err := snapshots.Save(projectionSnapshot(t, resources.registry)); err != nil {
		t.Fatal(err)
	}
	cmd := &sessionStateCommand{
		resources:    resources.store(),
		sessionStore: func() (sessionstate.Store, error) { return snapshots, nil },
		now:          func() time.Time { return resourceFixtureClock.Add(time.Hour) },
		runner:       &projectionMissingSessionRunner{},
		homeDir:      func() (string, error) { return t.TempDir(), nil },
		lookupEnv:    localeLookupEnv("ko_KR.UTF-8"),
	}
	var stdout bytes.Buffer
	if err := cmd.runRestore([]string{"--session", "saved-beta", "--project", "uid:prj-beta", "--dry-run"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"스냅샷 projection", "Window 1", "Registry 쓰기 0", "tmux 쓰기 0", "스냅샷 쓰기 0"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("Korean projection preview %q missing %q", stdout.String(), want)
		}
	}
	if resources.writes != 0 || resources.transactions != 0 {
		t.Fatalf("localized dry-run writes=%d transactions=%d", resources.writes, resources.transactions)
	}
}

func TestRestoreSnapshotRequiresExplicitSourceAndTarget(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "source", args: []string{"--project", "uid:prj-beta", "--dry-run"}, want: "explicit --session"},
		{name: "target", args: []string{"--session", "saved-beta", "--dry-run"}, want: "explicit --project"},
		{name: "one intent", args: []string{"--session", "saved-beta", "--project", "uid:prj-beta", "--dry-run", "--yes"}, want: "exactly one"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := &sessionStateCommand{}
			err := cmd.runRestore(tc.args, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("runRestore(%v) error=%v, want %q", tc.args, err, tc.want)
			}
		})
	}
}

func TestRestoreSnapshotRuntimeRefusalKeepsCommittedDesiredRegistryAndNotice(t *testing.T) {
	resources := newFakeResourceStore(t)
	snapshots := sessionstate.NewStore(t.TempDir())
	snap := projectionSnapshot(t, resources.registry)
	if err := snapshots.Save(snap); err != nil {
		t.Fatal(err)
	}
	runner := &projectionMissingSessionRunner{}
	topology := &fakeProjectTopologyMaterializer{err: errors.New("refused stale cwd")}
	reporter := &recordingProjectStartupReporter{}
	cmd := &sessionStateCommand{resources: resources.store(), sessionStore: func() (sessionstate.Store, error) { return snapshots, nil }, now: func() time.Time { return resourceFixtureClock.Add(time.Hour) }, runner: runner, projectTopology: topology, projectTrust: &projectionTrustAuthorizer{allowed: true}, notices: reporter, lookupEnv: func(string) string { return "" }}
	err := cmd.runRestore([]string{"--session", "saved-beta", "--project", "uid:prj-beta", "--yes"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "committed desired Registry") {
		t.Fatalf("error=%v", err)
	}
	if resources.writes != 1 {
		t.Fatalf("Registry writes=%d, want committed desired state", resources.writes)
	}
	window := resources.registry.WindowsOf("prj-beta")[0]
	pane := resources.registry.PanesOf(window.Metadata.UID)[0]
	if pane.Spec.CWD != "/srv/beta/restored" {
		t.Fatalf("desired Pane cwd=%q", pane.Spec.CWD)
	}
	if len(reporter.messages) != 1 || !strings.Contains(reporter.messages[0], "runtime item was refused") {
		t.Fatalf("notices=%q", reporter.messages)
	}
	loaded, err := snapshots.Load("saved-beta")
	if err != nil || loaded.Windows[0].Panes[0].CWD != "/srv/beta/restored" {
		t.Fatalf("source snapshot changed: %+v %v", loaded, err)
	}
}

func TestRestoreSnapshotRecordsCountsAndFinalExplicitClientHandoff(t *testing.T) {
	resources := newFakeResourceStore(t)
	snapshots := sessionstate.NewStore(t.TempDir())
	snap := projectionSnapshot(t, resources.registry)
	if err := snapshots.Save(snap); err != nil {
		t.Fatal(err)
	}
	snapshotPath, err := snapshots.Path("saved-beta")
	if err != nil {
		t.Fatal(err)
	}
	snapshotBefore, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	runner := &projectionMissingSessionRunner{}
	topology := &fakeProjectTopologyMaterializer{materialized: true}
	writer := &appLifecycleWriter{}
	lifecycle := diagnostics.NewLifecycleRecorder(writer, "projection-restore", "0.13.0", "tmux")
	cmd := &sessionStateCommand{diagnostics: lifecycle.SessionState(), resources: resources.store(), sessionStore: func() (sessionstate.Store, error) { return snapshots, nil }, now: func() time.Time { return resourceFixtureClock.Add(time.Hour) }, runner: runner, projectTopology: topology, projectTrust: &projectionTrustAuthorizer{allowed: true}, lookupEnv: func(string) string { return "" }}
	if err := cmd.runRestore([]string{"--session", "saved-beta", "--project", "uid:prj-beta", "--client", "/dev/pts/9", "--yes"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(writer.events) != 1 {
		t.Fatalf("diagnostic events=%#v", writer.events)
	}
	event := writer.events[0]
	if event.Operation != string(diagnostics.OperationSessionStateRestore) || event.Source != string(diagnostics.SessionStateSourceManual) || event.WindowCount == nil || *event.WindowCount != 1 || event.ShellRecipeCount == nil || *event.ShellRecipeCount != 1 {
		t.Fatalf("restore event=%#v", event)
	}
	last := runner.calls[len(runner.calls)-1]
	if strings.Join(last, " ") != "tmux -L projmux switch-client -c /dev/pts/9 -t =beta" {
		t.Fatalf("final handoff=%q", last)
	}
	snapshotAfter, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(snapshotBefore, snapshotAfter) {
		t.Fatalf("successful projection changed source snapshot bytes")
	}
}

func TestRestoreSnapshotRefusesLiveProjectIdentityBeforeCommit(t *testing.T) {
	resources := newFakeResourceStore(t)
	snapshots := sessionstate.NewStore(t.TempDir())
	snap := projectionSnapshot(t, resources.registry)
	if err := snapshots.Save(snap); err != nil {
		t.Fatal(err)
	}
	path, err := snapshots.Path("saved-beta")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	server := newFakeTmux()
	foreign := server.addSession("foreign-beta")
	foreign.opts["@projmux_project_uid"] = "prj-beta"
	foreign.opts["@projmux_project_path"] = "/srv/beta"
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00projmux": server}}
	cmd := &sessionStateCommand{
		resources: resources.store(), sessionStore: func() (sessionstate.Store, error) { return snapshots, nil },
		now: func() time.Time { return resourceFixtureClock.Add(time.Hour) }, runner: runner,
		projectTopology: &fakeProjectTopologyMaterializer{materialized: true},
		projectTrust:    &projectionTrustAuthorizer{allowed: true},
	}
	err = cmd.runRestore([]string{"--session", "saved-beta", "--project", "uid:prj-beta", "--yes"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "ownership preflight") {
		t.Fatalf("error=%v, want ownership preflight refusal", err)
	}
	if resources.transactions != 0 || resources.writes != 0 {
		t.Fatalf("preflight refusal transactions=%d writes=%d", resources.transactions, resources.writes)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("preflight refusal changed source snapshot bytes")
	}
}

func TestRestoreSnapshotRechecksClosedProjectAfterTrustBeforeCommit(t *testing.T) {
	resources := newFakeResourceStore(t)
	registryBefore := resources.registry.Clone()
	snapshots := sessionstate.NewStore(t.TempDir())
	snap := projectionSnapshot(t, resources.registry)
	if err := snapshots.Save(snap); err != nil {
		t.Fatal(err)
	}
	path, err := snapshots.Path("saved-beta")
	if err != nil {
		t.Fatal(err)
	}
	snapshotBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	server := newFakeTmux()
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00projmux": server}}
	authorizer := &projectionTrustAuthorizer{allowed: true, onAuthorize: func() {
		live := server.addSession("beta")
		live.opts["@projmux_project_uid"] = "prj-beta"
		live.opts["@projmux_project_path"] = "/srv/beta"
	}}
	cmd := &sessionStateCommand{
		resources: resources.store(), sessionStore: func() (sessionstate.Store, error) { return snapshots, nil },
		now: func() time.Time { return resourceFixtureClock.Add(time.Hour) }, runner: runner,
		projectTopology: &fakeProjectTopologyMaterializer{materialized: true}, projectTrust: authorizer,
	}
	err = cmd.runRestore([]string{"--session", "saved-beta", "--project", "uid:prj-beta", "--yes"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "live; close it before replacing desired state") {
		t.Fatalf("error=%v, want post-trust live Project refusal", err)
	}
	if resources.transactions != 0 || resources.writes != 0 {
		t.Fatalf("post-trust refusal transactions=%d writes=%d", resources.transactions, resources.writes)
	}
	if !reflect.DeepEqual(registryBefore, resources.registry) {
		t.Fatal("post-trust refusal changed Registry")
	}
	snapshotAfter, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(snapshotBefore, snapshotAfter) {
		t.Fatal("post-trust refusal changed source snapshot")
	}
}

func TestRestoreSnapshotTrustRefusalIsZeroWrite(t *testing.T) {
	for _, tc := range []struct {
		name    string
		allowed bool
		err     error
		want    string
	}{
		{name: "denied", want: "trust was denied"},
		{name: "authorizer error", err: errors.New("trust store unavailable"), want: "trust store unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resources := newFakeResourceStore(t)
			registryBefore := resources.registry.Clone()
			snapshots := sessionstate.NewStore(t.TempDir())
			snap := projectionSnapshot(t, resources.registry)
			if err := snapshots.Save(snap); err != nil {
				t.Fatal(err)
			}
			path, err := snapshots.Path("saved-beta")
			if err != nil {
				t.Fatal(err)
			}
			snapshotBefore, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			runner := &projectionMissingSessionRunner{}
			authorizer := &projectionTrustAuthorizer{allowed: tc.allowed, err: tc.err}
			cmd := &sessionStateCommand{
				resources: resources.store(), sessionStore: func() (sessionstate.Store, error) { return snapshots, nil },
				now: func() time.Time { return resourceFixtureClock.Add(time.Hour) }, runner: runner,
				projectTopology: &fakeProjectTopologyMaterializer{materialized: true}, projectTrust: authorizer,
			}
			err = cmd.runRestore([]string{"--session", "saved-beta", "--project", "uid:prj-beta", "--yes"}, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
			if len(authorizer.calls) != 1 || authorizer.calls[0] != "/srv/beta" {
				t.Fatalf("trust calls=%q", authorizer.calls)
			}
			if writes := projectionRunnerWriteCalls(runner.calls); len(writes) != 0 || resources.transactions != 0 || resources.writes != 0 {
				t.Fatalf("refusal tmux writes=%q all calls=%q transactions=%d Registry writes=%d", writes, runner.calls, resources.transactions, resources.writes)
			}
			if !reflect.DeepEqual(registryBefore, resources.registry) {
				t.Fatal("trust refusal changed Registry")
			}
			snapshotAfter, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(snapshotBefore, snapshotAfter) {
				t.Fatal("trust refusal changed source snapshot")
			}
		})
	}
}

func TestRestoreSnapshotInvalidProjectionRefusesBeforeTrustOrTmux(t *testing.T) {
	resources := newFakeResourceStore(t)
	registryBefore := resources.registry.Clone()
	snapshots := sessionstate.NewStore(t.TempDir())
	snap := projectionSnapshot(t, resources.registry)
	snap.Metadata.UID = "prj-alpha"
	if err := snapshots.Save(snap); err != nil {
		t.Fatal(err)
	}
	path, err := snapshots.Path("saved-beta")
	if err != nil {
		t.Fatal(err)
	}
	snapshotBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	runner := &projectionMissingSessionRunner{}
	authorizer := &projectionTrustAuthorizer{allowed: true}
	cmd := &sessionStateCommand{
		resources: resources.store(), sessionStore: func() (sessionstate.Store, error) { return snapshots, nil },
		now: func() time.Time { return resourceFixtureClock.Add(time.Hour) }, runner: runner,
		projectTopology: &fakeProjectTopologyMaterializer{materialized: true}, projectTrust: authorizer,
	}
	err = cmd.runRestore([]string{"--session", "saved-beta", "--project", "uid:prj-beta", "--yes"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "does not match target") {
		t.Fatalf("error=%v, want Project uid mismatch", err)
	}
	if len(authorizer.calls) != 0 || len(runner.calls) != 0 || resources.transactions != 0 || resources.writes != 0 {
		t.Fatalf("invalid projection trust=%q tmux=%q transactions=%d writes=%d", authorizer.calls, runner.calls, resources.transactions, resources.writes)
	}
	if !reflect.DeepEqual(registryBefore, resources.registry) {
		t.Fatal("invalid projection changed Registry")
	}
	snapshotAfter, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(snapshotBefore, snapshotAfter) {
		t.Fatal("invalid projection changed source snapshot")
	}
}
