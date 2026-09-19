package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

const absentServerSocket = "/tmp/fake-tmux/absent"

// absentServerRunner is a tmux that answers every call with failure, the way
// a client does when no server is running behind the exact target. It records
// each argv so a test can prove the route issued no tmux write.
type absentServerRunner struct {
	failure func(args []string) ([]byte, error)
	calls   [][]string
}

func (r *absentServerRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	if name != "tmux" {
		return nil, fmt.Errorf("absent-server fake runs only tmux, got %s", name)
	}
	r.calls = append(r.calls, slices.Clone(args))
	return r.failure(args)
}

// writes returns every recorded call whose tmux verb is not a read.
func (r *absentServerRunner) writes() [][]string {
	reads := []string{"display-message", "show-options", "list-sessions", "list-windows", "list-panes", "has-session"}
	var out [][]string
	for _, call := range r.calls {
		args := call
		for len(args) >= 2 && (args[0] == "-L" || args[0] == "-S") {
			args = args[2:]
		}
		if len(args) == 0 || !slices.Contains(reads, args[0]) {
			out = append(out, call)
		}
	}
	return out
}

func failEveryCall(err error) func([]string) ([]byte, error) {
	return func([]string) ([]byte, error) { return nil, err }
}

func noServerFailure(socketPath string) error {
	return appTypedCommandFailure{failure: inttmux.CommandFailure{
		Kind: inttmux.CommandFailureExit, Stderr: "no server running on " + socketPath,
	}}
}

// absentServerProjects is the Registry every absent-server test starts from:
// two Projects recorded live on the absent socket, and one each recorded live
// on another server, live on no known server, and already offline on the
// absent socket.
type absentServerProjects struct {
	store                                     *fakeResourceStore
	gone, second, elsewhere, unknown, offline string
}

func newAbsentServerProjects(t *testing.T, socketPath string) absentServerProjects {
	t.Helper()
	store := &fakeResourceStore{registry: coremetadata.NewRegistry(), dirs: map[string]bool{}, now: resourceFixtureClock}
	fixture := absentServerProjects{
		store:     store,
		gone:      addEndedSessionProject(t, store, "gone", socketPath),
		second:    addEndedSessionProject(t, store, "second", socketPath),
		elsewhere: addEndedSessionProject(t, store, "elsewhere", endedSessionOtherSocket),
		unknown:   addEndedSessionProject(t, store, "unknown", ""),
		offline:   addEndedSessionProject(t, store, "offline", socketPath),
	}
	project, _ := store.registry.Project(fixture.offline)
	session := *project.Status.Session
	session.Live = false
	project.Status.Session = &session
	return fixture
}

func (f absentServerProjects) untouched() map[string]bool {
	return map[string]bool{f.elsewhere: true, f.unknown: true, f.offline: true}
}

func absentServerCommand(store *fakeResourceStore, runner tmuxCommandRunner, env map[string]string) *resourceReconcileCommand {
	return &resourceReconcileCommand{
		runner: runner, resources: store.store(),
		lookupEnv: func(key string) string { return env[key] },
	}
}

func decodeReconcileReport(t *testing.T, out string) resourceReconcileReport {
	t.Helper()
	var report resourceReconcileReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("reconcile json: %v\n%s", err, out)
	}
	return report
}

// TestResourceReconcileLowersProjectionsRecordedOnTheExactSocketWhenItsServerIsNotRunning
// is the stale live flag after an exact `kill-server`: every projection
// recorded live on the absent server's own socket is lowered in one Registry
// commit, keeping its session name and socketPath, and the receipt states the
// absent server and the lowered count. Projects recorded on another, on no
// known, or already offline are byte-identical, and no tmux write is issued.
// Both no-server phrasings tmux prints take the same route.
func TestResourceReconcileLowersProjectionsRecordedOnTheExactSocketWhenItsServerIsNotRunning(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		err  error
	}{
		{"no server running", noServerFailure(absentServerSocket)},
		{"connection refused", appTypedCommandFailure{failure: inttmux.CommandFailure{
			Kind: inttmux.CommandFailureExit, Stderr: "failed to connect to server: connection refused",
		}}},
		{"untyped no server running", errors.New("tmux display-message: exit status 1: no server running on " + absentServerSocket)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fixture := newAbsentServerProjects(t, absentServerSocket)
			store := fixture.store
			// The commit moves the Registry-wide UpdatedAt; the untouched graphs
			// themselves must not move at all.
			untouchedGraph := func() coremetadata.Registry {
				graph := resourceRegistryProjectGraph(store.registry, fixture.untouched())
				graph.UpdatedAt = time.Time{}
				return graph
			}
			graphBefore := untouchedGraph()
			runner := &absentServerRunner{failure: failEveryCall(tc.err)}
			command := absentServerCommand(store, runner, nil)
			store.now = resourceFixtureClock.Add(time.Hour)

			out, _, err := runReconcile(t, command, "resources", "--socket-path", absentServerSocket, "-o", "json")
			if err != nil {
				t.Fatalf("absent-server reconcile: %v\n%s", err, out)
			}
			report := decodeReconcileReport(t, out)
			wantStages := []string{
				"exact server absent: " + absentServerSocket,
				"server absent: lowered 2 session projection(s) recorded on " + absentServerSocket,
				"Registry commit",
				"registry:update:project:project/gone",
				"registry:update:project:project/second",
			}
			if report.Outcome != "changed" || !reflect.DeepEqual(report.CompletedStages, wantStages) || report.Counts.Changed != 2 || report.Error != "" {
				t.Fatalf("report outcome=%s stages=%q counts=%+v error=%q, want changed with %q", report.Outcome, report.CompletedStages, report.Counts, report.Error, wantStages)
			}
			for _, item := range report.Items {
				name := strings.TrimPrefix(item.Target, "project/")
				want := `session "` + name + `" is recorded live on the exact socket ` + absentServerSocket + `, whose server is not running`
				if item.Field != "status.session.live" || item.Before != "true" || item.After != "false" || item.Outcome != "changed" || !strings.Contains(item.Reason, want) {
					t.Fatalf("lower item = %+v, want status.session.live true -> false with reason %q", item, want)
				}
			}
			if report.Reobserved != nil || report.HostMode != "" {
				t.Fatalf("absent-server run claimed a reobservation or host: %+v %q", report.Reobserved, report.HostMode)
			}
			for _, uid := range []string{fixture.gone, fixture.second} {
				project, _ := store.registry.Project(uid)
				want := coremetadata.SessionProjection{Name: project.Metadata.Name, SocketPath: absentServerSocket}
				if got := storedSessionProjection(t, store, uid); got != want {
					t.Fatalf("lowered projection = %+v, want %+v", got, want)
				}
			}
			if !store.registry.UpdatedAt.Equal(store.now) || store.writes != 1 {
				t.Fatalf("Registry UpdatedAt=%s writes=%d, want one commit at %s", store.registry.UpdatedAt, store.writes, store.now)
			}
			if graphAfter := untouchedGraph(); !reflect.DeepEqual(graphBefore, graphAfter) {
				t.Fatalf("Projects on another, no, or an offline projection changed:\n--- before ---\n%#v\n--- after ---\n%#v", graphBefore, graphAfter)
			}
			if writes := runner.writes(); len(writes) != 0 {
				t.Fatalf("absent-server reconcile issued tmux writes: %q", writes)
			}

			human, _, err := runReconcile(t, absentServerCommand(store, runner, nil), "resources", "--socket-path", absentServerSocket)
			if err == nil {
				t.Fatalf("second run after the lower did not report the absent server:\n%s", human)
			}
		})
	}
}

// TestResourceReconcileHumanReceiptStatesTheAbsentServerAndLoweredCount pins
// the human rendering of the same two facts the JSON receipt carries.
func TestResourceReconcileHumanReceiptStatesTheAbsentServerAndLoweredCount(t *testing.T) {
	t.Parallel()

	fixture := newAbsentServerProjects(t, absentServerSocket)
	runner := &absentServerRunner{failure: failEveryCall(noServerFailure(absentServerSocket))}
	out, _, err := runReconcile(t, absentServerCommand(fixture.store, runner, nil), "resources", "--socket-path", absentServerSocket)
	if err != nil {
		t.Fatalf("absent-server reconcile: %v\n%s", err, out)
	}
	for _, want := range []string{
		"outcome: changed",
		"- exact server absent: " + absentServerSocket,
		"- server absent: lowered 2 session projection(s) recorded on " + absentServerSocket,
		"- Registry commit\n",
		`status.session.live true -> false (session "gone" is recorded live on the exact socket ` + absentServerSocket + ", whose server is not running",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("human receipt missing %q:\n%s", want, out)
		}
	}
}

// TestResourceReconcileWithNothingToLowerOnAnAbsentServerFailsExactlyAsBefore
// is the repeat: once nothing recorded live on the absent socket is left, the
// run fails at the runtime authority stage with the original error and
// report, opens no Registry transaction, and leaves UpdatedAt as it was.
func TestResourceReconcileWithNothingToLowerOnAnAbsentServerFailsExactlyAsBefore(t *testing.T) {
	t.Parallel()

	fixture := newAbsentServerProjects(t, absentServerSocket)
	store := fixture.store
	bindErr := noServerFailure(absentServerSocket)
	runner := &absentServerRunner{failure: failEveryCall(bindErr)}
	if out, _, err := runReconcile(t, absentServerCommand(store, runner, nil), "resources", "--socket-path", absentServerSocket); err != nil {
		t.Fatalf("first absent-server reconcile: %v\n%s", err, out)
	}
	settled, updatedAt, writes, transactions := store.snapshot(), store.registry.UpdatedAt, store.writes, store.transactions
	settledRegistry := store.registry.Clone()
	store.now = resourceFixtureClock.Add(24 * time.Hour)

	wantErr := "reconcile resources failed at runtime authority: " + bindErr.Error()
	var outputs []string
	for range 2 {
		out, _, err := runReconcile(t, absentServerCommand(store, runner, nil), "resources", "--socket-path", absentServerSocket, "-o", "json")
		if err == nil || err.Error() != wantErr {
			t.Fatalf("repeat err = %v, want %q\n%s", err, wantErr, out)
		}
		report := decodeReconcileReport(t, out)
		if report.Outcome != "failed" || len(report.CompletedStages) != 0 || len(report.Items) != 0 ||
			!strings.HasPrefix(report.Error, bindErr.Error()+"; remaining drift unavailable: ") {
			t.Fatalf("repeat report is not the runtime authority failure: %+v", report)
		}
		outputs = append(outputs, out)
	}
	if outputs[0] != outputs[1] {
		t.Fatalf("repeat reports differ:\n%s\n%s", outputs[0], outputs[1])
	}
	if store.snapshot() != settled || !store.registry.UpdatedAt.Equal(updatedAt) || store.writes != writes || store.transactions != transactions ||
		!reflect.DeepEqual(store.registry, settledRegistry) {
		t.Fatalf("repeat touched the Registry: writes %d -> %d, transactions %d -> %d, UpdatedAt %s -> %s",
			writes, store.writes, transactions, store.transactions, updatedAt, store.registry.UpdatedAt)
	}
}

// TestAbsentServerConvergeWritesNoRegistryBytesWhenTheLockedRegistryHasNothingToLower
// covers the race the unlocked preview cannot: another writer lowered the
// projection between the snapshot and the lock. The transaction aborts, the
// Registry file keeps its bytes and mtime, and the run reports the original
// runtime authority error.
func TestAbsentServerConvergeWritesNoRegistryBytesWhenTheLockedRegistryHasNothingToLower(t *testing.T) {
	t.Parallel()

	fixture := newAbsentServerProjects(t, absentServerSocket)
	stale := fixture.store.registry.Clone()
	settled := stale.Clone()
	fixture.store.mutator().LowerProjectSessionsEndedOnServer(&settled, absentServerSocket, map[string]bool{})

	path := filepath.Join(t.TempDir(), "registry.json")
	file := intmetadata.NewStore(path)
	if _, err := file.Update(func(registry *coremetadata.Registry) error { *registry = settled.Clone(); return nil }); err != nil {
		t.Fatalf("seed Registry file: %v", err)
	}
	bytesBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	infoBefore, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	store := &resourceStore{
		snapshot:         func() (coremetadata.Registry, error) { return stale.Clone(), nil },
		updateConvergent: file.UpdateConvergent,
		mutator:          fixture.store.mutator,
	}
	bindErr := noServerFailure(absentServerSocket)
	target := tmuxTransport{Kind: tmuxSocketPath, Value: absentServerSocket, Source: tmuxSocketPathSource}
	kernel := newResourceControllerKernel(&absentServerRunner{failure: failEveryCall(bindErr)}, store, resourceReconcilePlanner{}, target)
	kernel.lookupEnv = func(string) string { return "" }
	_, err = kernel.converge(context.Background())
	var runErr *controllerRunError
	if !errors.As(err, &runErr) || runErr.stage != "runtime authority" || !errors.Is(runErr.err, bindErr) || len(runErr.run.completed) != 0 {
		t.Fatalf("converge err = %#v, want the runtime authority failure", err)
	}
	bytesAfter, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	infoAfter, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytesBefore, bytesAfter) || !infoAfter.ModTime().Equal(infoBefore.ModTime()) {
		t.Fatal("aborted absent-server transaction rewrote the Registry file")
	}
}

// TestResourceReconcileKeepsTheRuntimeAuthorityFailureWhenTheServerIsNotProvablyAbsent
// injects every runtime authority failure that is not a missing server:
// a permission-denied connect, a deadline, and a server that answers but is
// not app-owned. Each keeps today's stage and message and lowers nothing.
func TestResourceReconcileKeepsTheRuntimeAuthorityFailureWhenTheServerIsNotProvablyAbsent(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		failure func([]string) ([]byte, error)
		want    string
	}{
		{
			name: "permission denied",
			failure: failEveryCall(appTypedCommandFailure{failure: inttmux.CommandFailure{
				Kind: inttmux.CommandFailureExit, Stderr: "error connecting to " + absentServerSocket + " (Permission denied)",
			}}),
			want: "typed tmux command failure",
		},
		{
			name:    "untyped permission denied",
			failure: failEveryCall(errors.New("tmux: exit status 1: error connecting to " + absentServerSocket + " (Permission denied)")),
			want:    "(Permission denied)",
		},
		{
			name:    "context deadline",
			failure: failEveryCall(fmt.Errorf("tmux display-message: %w", context.DeadlineExceeded)),
			want:    context.DeadlineExceeded.Error(),
		},
		{
			name: "not app-owned",
			failure: func(args []string) ([]byte, error) {
				switch {
				case slices.Contains(args, "display-message"):
					return []byte(absentServerSocket + "\n"), nil
				case slices.Contains(args, tmuxopts.AppGlobal):
					return []byte("foreign\n"), nil
				default:
					return []byte("\n"), nil
				}
			},
			want: "exact server is not app-owned",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fixture := newAbsentServerProjects(t, absentServerSocket)
			store := fixture.store
			before, registryBefore, writes, transactions := store.snapshot(), store.registry.Clone(), store.writes, store.transactions
			runner := &absentServerRunner{failure: tc.failure}
			for _, dryRun := range []bool{false, true} {
				args := []string{"resources", "--socket-path", absentServerSocket, "-o", "json"}
				if dryRun {
					args = append(args, "--dry-run")
				}
				out, _, err := runReconcile(t, absentServerCommand(store, runner, nil), args...)
				if strings.Contains(out, "server absent") || strings.Contains(out, "whose server is not running") {
					t.Fatalf("dry-run=%t planned an absent-server lower:\n%s", dryRun, out)
				}
				if !dryRun && (err == nil || !strings.HasPrefix(err.Error(), "reconcile resources failed at runtime authority: ") || !strings.Contains(err.Error(), tc.want)) {
					t.Fatalf("err = %v, want the runtime authority failure containing %q\n%s", err, tc.want, out)
				}
			}
			if store.snapshot() != before || !reflect.DeepEqual(store.registry, registryBefore) || store.writes != writes || store.transactions != transactions {
				t.Fatalf("a non-missing failure touched the Registry: writes %d -> %d, transactions %d -> %d", writes, store.writes, transactions, store.transactions)
			}
			if storedSessionProjection(t, store, fixture.gone).Live != true {
				t.Fatal("a non-missing failure lowered a projection")
			}
			if w := runner.writes(); len(w) != 0 {
				t.Fatalf("issued tmux writes: %q", w)
			}
		})
	}
}

// TestResourceReconcileDryRunPreviewsTheAbsentServerLowerAndWritesNothing
// previews the same lower as planned items; with nothing to lower the preview
// fails exactly as it did before.
func TestResourceReconcileDryRunPreviewsTheAbsentServerLowerAndWritesNothing(t *testing.T) {
	t.Parallel()

	fixture := newAbsentServerProjects(t, absentServerSocket)
	store := fixture.store
	runner := &absentServerRunner{failure: failEveryCall(noServerFailure(absentServerSocket))}
	before, registryBefore, transactions := store.snapshot(), store.registry.Clone(), store.transactions

	out, _, err := runReconcile(t, absentServerCommand(store, runner, nil), "resources", "--dry-run", "--socket-path", absentServerSocket, "-o", "json")
	if err != nil {
		t.Fatalf("absent-server dry-run: %v\n%s", err, out)
	}
	report := decodeReconcileReport(t, out)
	var keys []string
	for _, item := range report.Items {
		keys = append(keys, item.Key)
		if item.Outcome != "planned" || item.Field != "status.session.live" || !strings.Contains(item.Reason, "whose server is not running") {
			t.Fatalf("preview item = %+v, want a planned lower", item)
		}
	}
	if want := []string{"registry:update:project:project/gone", "registry:update:project:project/second"}; !report.DryRun || report.Outcome != "planned" || !reflect.DeepEqual(keys, want) {
		t.Fatalf("preview dryRun=%t outcome=%s keys=%q, want planned %q", report.DryRun, report.Outcome, keys, want)
	}
	if store.snapshot() != before || !reflect.DeepEqual(store.registry, registryBefore) || store.transactions != transactions || len(runner.writes()) != 0 {
		t.Fatal("absent-server dry-run wrote")
	}

	settled := newAbsentServerProjects(t, absentServerSocket)
	settled.store.mutator().LowerProjectSessionsEndedOnServer(&settled.store.registry, absentServerSocket, map[string]bool{})
	emptyOut, _, emptyErr := runReconcile(t, absentServerCommand(settled.store, runner, nil), "resources", "--dry-run", "--socket-path", absentServerSocket, "-o", "json")
	if emptyErr == nil || !strings.HasPrefix(emptyErr.Error(), "plan resource reconciliation: ") || emptyOut != "" {
		t.Fatalf("dry-run with nothing to lower = %v, want the unchanged plan failure\n%s", emptyErr, emptyOut)
	}
}

// TestResourceReconcileResolvesTheAbsentServerPathFromTheExactTarget drives
// the three ways a target names a server that is not running: --socket-path,
// an inherited TMUX, and --socket <name> under TMUX_TMPDIR. Each lowers only
// the Project recorded on the path that target resolves to.
func TestResourceReconcileResolvesTheAbsentServerPathFromTheExactTarget(t *testing.T) {
	t.Parallel()

	tmpdir := t.TempDir()
	labelDir := filepath.Join(tmpdir, fmt.Sprintf("tmux-%d", os.Getuid()))
	if err := os.Mkdir(labelDir, 0o700); err != nil {
		t.Fatal(err)
	}
	resolvedLabelDir, err := filepath.EvalSymlinks(labelDir)
	if err != nil {
		t.Fatal(err)
	}
	labelSocket := filepath.Join(resolvedLabelDir, "absent")

	for _, tc := range []struct {
		name     string
		args     []string
		env      map[string]string
		recorded string
	}{
		{"socket path", []string{"--socket-path", absentServerSocket}, nil, absentServerSocket},
		{"inherited TMUX", nil, map[string]string{"TMUX": absentServerSocket + ",4242,0"}, absentServerSocket},
		{"socket name under TMUX_TMPDIR", []string{"--socket", "absent"}, map[string]string{"TMUX_TMPDIR": tmpdir}, labelSocket},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fixture := newAbsentServerProjects(t, tc.recorded)
			decoy := addEndedSessionProject(t, fixture.store, "decoy", "/tmp/fake-tmux/decoy")
			runner := &absentServerRunner{failure: failEveryCall(noServerFailure(tc.recorded))}
			args := append([]string{"resources"}, tc.args...)
			out, _, err := runReconcile(t, absentServerCommand(fixture.store, runner, tc.env), args...)
			if err != nil || !strings.Contains(out, "- exact server absent: "+tc.recorded+"\n") {
				t.Fatalf("reconcile %q: %v\n%s", args, err, out)
			}
			for uid, live := range map[string]bool{fixture.gone: false, fixture.second: false, fixture.elsewhere: true, decoy: true} {
				if got := storedSessionProjection(t, fixture.store, uid).Live; got != live {
					t.Fatalf("%s live = %t, want %t", uid, got, live)
				}
			}
		})
	}
}

// TestAbsentServerSocketPathFollowsTmuxLabelResolution pins the resolver:
// -S is its own clean absolute path, and -L <name> is tmux's own label path
// under the first usable TMUX_TMPDIR or default base, never created and
// resolved through symlinks.
func TestAbsentServerSocketPathFollowsTmuxLabelResolution(t *testing.T) {
	t.Parallel()

	uid := os.Getuid()
	labelDir := func(t *testing.T, base string, mode os.FileMode) string {
		t.Helper()
		dir := filepath.Join(base, fmt.Sprintf("tmux-%d", uid))
		if err := os.Mkdir(dir, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatal(err)
		}
		return resolved
	}
	label := func(name string) tmuxTransport { return tmuxTransport{Kind: tmuxSocketName, Value: name} }
	env := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}

	withTmpdir, fallback := t.TempDir(), t.TempDir()
	withTmpdirDir, fallbackDir := labelDir(t, withTmpdir, 0o700), labelDir(t, fallback, 0o700)
	missing := t.TempDir()

	linkParent := t.TempDir()
	linkTarget := t.TempDir()
	linkTargetDir := labelDir(t, linkTarget, 0o700)
	linkedBase := filepath.Join(linkParent, "linked")
	if err := os.Symlink(linkTarget, linkedBase); err != nil {
		t.Fatal(err)
	}

	symlinkedLabel := t.TempDir()
	if err := os.Symlink(fallback, filepath.Join(symlinkedLabel, fmt.Sprintf("tmux-%d", uid))); err != nil {
		t.Fatal(err)
	}
	shared := t.TempDir()
	labelDir(t, shared, 0o707)

	for _, tc := range []struct {
		name     string
		target   tmuxTransport
		env      map[string]string
		fallback string
		want     string
	}{
		{"socket path is itself", tmuxTransport{Kind: tmuxSocketPath, Value: "/srv/tmux/sock"}, nil, fallback, "/srv/tmux/sock"},
		{"unclean socket path", tmuxTransport{Kind: tmuxSocketPath, Value: "/srv/tmux/../sock"}, nil, fallback, ""},
		{"relative socket path", tmuxTransport{Kind: tmuxSocketPath, Value: "tmux/sock"}, nil, fallback, ""},
		{"TMUX_TMPDIR set", label("work"), map[string]string{"TMUX_TMPDIR": withTmpdir}, fallback, filepath.Join(withTmpdirDir, "work")},
		{"TMUX_TMPDIR unset uses the default base", label("work"), nil, fallback, filepath.Join(fallbackDir, "work")},
		{"relative TMUX_TMPDIR is ignored", label("work"), map[string]string{"TMUX_TMPDIR": "relative"}, fallback, filepath.Join(fallbackDir, "work")},
		{"TMUX_TMPDIR without a label dir falls back", label("work"), map[string]string{"TMUX_TMPDIR": missing}, fallback, filepath.Join(fallbackDir, "work")},
		{"symlinked base resolves", label("work"), map[string]string{"TMUX_TMPDIR": linkedBase}, missing, filepath.Join(linkTargetDir, "work")},
		{"symlinked label dir is refused", label("work"), map[string]string{"TMUX_TMPDIR": symlinkedLabel}, missing, ""},
		{"label dir open to others is refused", label("work"), map[string]string{"TMUX_TMPDIR": shared}, missing, ""},
		{"missing everywhere", label("work"), map[string]string{"TMUX_TMPDIR": missing}, missing, ""},
		{"empty name", label(""), map[string]string{"TMUX_TMPDIR": withTmpdir}, fallback, ""},
		{"name with a slash", label("a/b"), map[string]string{"TMUX_TMPDIR": withTmpdir}, fallback, ""},
		{"no target", tmuxTransport{}, nil, fallback, ""},
	} {
		if got := absentServerSocketPath(tc.target, env(tc.env), tc.fallback, uid); got != tc.want {
			t.Errorf("%s: absentServerSocketPath = %q, want %q", tc.name, got, tc.want)
		}
	}
	if _, err := os.Stat(filepath.Join(missing, fmt.Sprintf("tmux-%d", uid))); !os.IsNotExist(err) {
		t.Fatalf("resolver created a label directory: %v", err)
	}
}

// TestResourceReconcileNeverLowersAgainstAnAbsentServerPathThatDoesNotMatch
// pins the attribution: a missing server lowers only projections recorded on
// the exact path its target resolves to. A -L target that resolves elsewhere
// (or not at all) leaves every projection recorded on another path live and
// fails exactly as before.
func TestResourceReconcileNeverLowersAgainstAnAbsentServerPathThatDoesNotMatch(t *testing.T) {
	t.Parallel()

	fixture := newAbsentServerProjects(t, absentServerSocket)
	store := fixture.store
	bindErr := noServerFailure(absentServerSocket)
	runner := &absentServerRunner{failure: failEveryCall(bindErr)}
	before, transactions := store.snapshot(), store.transactions
	env := map[string]string{"TMUX_TMPDIR": t.TempDir()}
	out, _, err := runReconcile(t, absentServerCommand(store, runner, env), "resources", "--socket", "absent")
	if err == nil || err.Error() != "reconcile resources failed at runtime authority: "+bindErr.Error() || strings.Contains(out, "server absent") {
		t.Fatalf("unmatched -L target err = %v, want the runtime authority failure\n%s", err, out)
	}
	if store.snapshot() != before || store.transactions != transactions || !storedSessionProjection(t, store, fixture.gone).Live {
		t.Fatal("an unmatched absent-server path touched the Registry")
	}
}
