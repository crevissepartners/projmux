package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
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
	cmd := &sessionStateCommand{resources: resources.store(), sessionStore: func() (sessionstate.Store, error) { return snapshots, nil }, now: func() time.Time { return resourceFixtureClock.Add(time.Hour) }}
	var stdout bytes.Buffer
	if err := cmd.runRestore([]string{"--session", "saved-beta", "--project", "uid:prj-beta", "--dry-run"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if resources.writes != 0 || resources.transactions != 0 {
		t.Fatalf("dry-run writes=%d transactions=%d", resources.writes, resources.transactions)
	}
	for _, want := range []string{"replace Window", "preserve uid", "lose conversation pointer", "Registry writes 0 / tmux writes 0 / snapshot writes 0"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("preview %q missing %q", stdout.String(), want)
		}
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
	cmd := &sessionStateCommand{resources: resources.store(), sessionStore: func() (sessionstate.Store, error) { return snapshots, nil }, now: func() time.Time { return resourceFixtureClock.Add(time.Hour) }, runner: runner, projectTopology: topology, notices: reporter, lookupEnv: func(string) string { return "" }}
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
	cmd := &sessionStateCommand{diagnostics: lifecycle.SessionState(), resources: resources.store(), sessionStore: func() (sessionstate.Store, error) { return snapshots, nil }, now: func() time.Time { return resourceFixtureClock.Add(time.Hour) }, runner: runner, projectTopology: topology, lookupEnv: func(string) string { return "" }}
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
