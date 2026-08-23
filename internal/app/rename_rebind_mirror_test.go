package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

type fakeMutationMirror struct {
	projectTarget string
	windowTarget  string
	paneTarget    string
	lookupErr     error
	writeErr      error
	calls         []string
}

type mutationExitError struct{}

func (mutationExitError) Error() string { return "tmux exited 77" }
func (mutationExitError) ExitCode() int { return 77 }

type mutationRoutingRunner struct {
	calls                               [][]string
	stableName, windowName, logicalPath string
	appMarker, logicalName              string
	windowUID, projectUID, sessionRole  string
	windowID, sessionID                 string
	listWindowReads, driftAt            int
	driftWindowID, driftSessionID       string
}

func (r *mutationRoutingRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	argv := tmuxCommandArgv(args)
	joined := strings.Join(argv, " ")
	if r.windowName == "" {
		r.windowName = "Runtime Review"
	}
	if r.stableName == "" {
		r.stableName = "review"
	}
	if r.windowID == "" {
		r.windowID = "@7"
	}
	if r.sessionID == "" {
		r.sessionID = "$1"
	}
	if r.windowUID == "" {
		r.windowUID = "win-alpha-review"
	}
	if r.projectUID == "" {
		r.projectUID = "prj-alpha"
	}
	switch {
	case strings.Contains(joined, "#{socket_path}") && strings.Contains(joined, "#{pid}") && strings.Contains(joined, "#{session_id}"):
		return []byte(strings.Join([]string{r.logicalPath, "4242", "$1", "@7", "%7"}, tmuxRowSep) + "\n"), nil
	case strings.Contains(joined, "display-message -p -F #{socket_path}"):
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-S" {
				return []byte(args[i+1] + "\n"), nil
			}
			if args[i] == "-L" {
				path := r.logicalPath
				if path == "" {
					path = "/tmp/projmux.sock"
				}
				return []byte(path + "\n"), nil
			}
		}
	case strings.Contains(joined, "show-options -gqv "+tmuxopts.AppGlobal):
		marker := r.appMarker
		if marker == "" {
			marker = "1"
		}
		return []byte(marker + "\n"), nil
	case strings.Contains(joined, "show-options -gqv "+runtimeMutationSocketNameOption):
		logical := r.logicalName
		if logical == "" {
			logical = defaultAppSocket
		}
		return []byte(logical + "\n"), nil
	case strings.Contains(joined, "display-message -p -F #{pid}"):
		return []byte("4242\n"), nil
	case strings.Contains(joined, "list-windows") && strings.Contains(joined, tmuxopts.ProjectUIDSession):
		r.listWindowReads++
		if r.driftAt > 0 && r.listWindowReads == r.driftAt {
			r.windowID, r.sessionID = r.driftWindowID, r.driftSessionID
		}
		return []byte(strings.Join([]string{r.windowID, r.windowUID, r.sessionID, r.projectUID, r.sessionRole, r.stableName, r.windowName}, tmuxRowSep) + "\n"), nil
	case len(argv) > 0 && argv[0] == "set-option" && slices.Contains(argv, tmuxopts.WindowName):
		r.stableName = argv[len(argv)-1]
		return nil, nil
	}
	for _, arg := range argv {
		switch arg {
		case "list-sessions":
			return []byte("prj-alpha\\037$1\\037alpha\n"), nil
		case "list-windows":
			return []byte("win-alpha-review\\037@7\\037alpha\\0371\n"), nil
		case "list-panes":
			return []byte("pan-alpha-log\\037%7\n"), nil
		}
	}
	return nil, nil
}

func (f *fakeMutationMirror) FindSessionForProjectUID(_ context.Context, uid string) (string, bool, error) {
	f.calls = append(f.calls, "find project "+uid)
	return f.projectTarget, f.projectTarget != "", f.lookupErr
}

func (f *fakeMutationMirror) FindWindowTargetForUID(_ context.Context, uid string) (string, bool, error) {
	f.calls = append(f.calls, "find window "+uid)
	return f.windowTarget, f.windowTarget != "", f.lookupErr
}

func (f *fakeMutationMirror) FindPaneTargetForUID(_ context.Context, uid string) (string, bool, error) {
	f.calls = append(f.calls, "find pane "+uid)
	return f.paneTarget, f.paneTarget != "", f.lookupErr
}

func (f *fakeMutationMirror) RenameProject(_ context.Context, target, name string) error {
	f.calls = append(f.calls, "rename project "+target+" "+name)
	return f.writeErr
}

func (f *fakeMutationMirror) RenameWindow(_ context.Context, target, name string) error {
	f.calls = append(f.calls, "rename window "+target+" "+name)
	return f.writeErr
}

func (f *fakeMutationMirror) RenamePane(_ context.Context, target, name string) error {
	f.calls = append(f.calls, "rename pane "+target+" "+name)
	return f.writeErr
}

func (f *fakeMutationMirror) RebindProject(_ context.Context, target, root string) error {
	f.calls = append(f.calls, "rebind project "+target+" "+root)
	return f.writeErr
}

func TestRenameImmediatelyConvergesOnlyTheStableNameMirror(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		args   []string
		mirror *fakeMutationMirror
		want   []string
	}{
		{name: "project", args: []string{"project", "alpha", "--name", "renamed"}, mirror: &fakeMutationMirror{projectTarget: "$1"}, want: []string{"find project prj-alpha", "rename project $1 renamed"}},
		{name: "window", args: []string{"window", "review", "--project", "alpha", "--name", "renamed"}, mirror: &fakeMutationMirror{windowTarget: "@7"}},
		{name: "pane", args: []string{"pane", "log", "--project", "alpha", "--window", "main", "--name", "renamed"}, mirror: &fakeMutationMirror{paneTarget: "%7"}, want: []string{"find pane pan-alpha-log", "rename pane %7 renamed"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			cmd := newTestRenameCommand(store)
			cmd.mirror = test.mirror
			var typedRunner *mutationRoutingRunner
			if test.name == "window" {
				typedRunner = &mutationRoutingRunner{}
				cmd.tmuxRunner = typedRunner
				cmd.lookupEnv = func(string) string { return "" }
			}
			if _, stderr, err := runRoute(t, cmd, test.args...); err != nil {
				t.Fatalf("rename error = %v (stderr=%s)", err, stderr)
			}
			if !reflect.DeepEqual(test.mirror.calls, test.want) {
				t.Fatalf("mirror calls = %v, want %v", test.mirror.calls, test.want)
			}
			if typedRunner != nil && (typedRunner.stableName != "renamed" || typedRunner.windowName != "Runtime Review") {
				t.Fatalf("typed Window rename stable/display effects = %q/%q, want renamed/Runtime Review", typedRunner.stableName, typedRunner.windowName)
			}
		})
	}
}

func TestWindowRenameAlreadyMatchingEffectStillRefusesRecycledRuntimeHandle(t *testing.T) {
	store := newFakeResourceStore(t)
	runner := &mutationRoutingRunner{
		stableName: "renamed", windowID: "@7", sessionID: "$1",
		driftAt: 2, driftWindowID: "@8", driftSessionID: "$2",
	}
	cmd := newTestRenameCommand(store)
	cmd.mirror = &fakeMutationMirror{windowTarget: "@7"}
	cmd.tmuxRunner = runner
	cmd.lookupEnv = func(string) string { return "" }

	_, _, err := runRoute(t, cmd, "window", "review", "--project", "alpha", "--name", "renamed")
	if err == nil || !strings.Contains(err.Error(), "Window runtime identity drifted before rename") {
		t.Fatalf("recycled already-matching Window rename = %v", err)
	}
	for _, call := range runner.calls {
		if slices.Contains(tmuxCommandArgv(call[1:]), "set-option") {
			t.Fatalf("recycled already-matching Window reached a write: %#v", runner.calls)
		}
	}
}

func TestWindowRenameUsesInheritedAppPIDAuthorityWithoutPaneReceipt(t *testing.T) {
	store := newFakeResourceStore(t)
	path := filepath.Join(t.TempDir(), "nondefault-app.sock")
	runner := &mutationRoutingRunner{logicalPath: path, logicalName: "rename-it"}
	cmd := newTestRenameCommand(store)
	cmd.mirror = &fakeMutationMirror{windowTarget: "@7"}
	cmd.tmuxRunner = runner
	cmd.lookupEnv = func(key string) string {
		if key == "TMUX" {
			return path + ",4242,0"
		}
		return ""
	}

	if _, stderr, err := runRoute(t, cmd, "window", "review", "--project", "alpha", "--name", "stable-window"); err != nil {
		t.Fatalf("PID-authorized Window rename: %v stderr=%s", err, stderr)
	}
	if runner.stableName != "stable-window" || runner.windowName != "Runtime Review" {
		t.Fatalf("stable/display names = %q/%q", runner.stableName, runner.windowName)
	}
	for _, call := range runner.calls {
		argv := tmuxCommandArgv(call[1:])
		if slices.Contains(argv, "set-option") && (len(call) < 3 || call[1] != "-S" || call[2] != path) {
			t.Fatalf("Window rename write escaped inherited physical route: %#v", call)
		}
	}

	for _, tc := range []struct {
		name   string
		tmux   string
		mutate func(*mutationRoutingRunner)
	}{
		{"PID mismatch", path + ",9999,0", func(*mutationRoutingRunner) {}},
		{"path mismatch", filepath.Join(t.TempDir(), "foreign.sock") + ",4242,0", func(*mutationRoutingRunner) {}},
		{"partial marker", path + ",4242,0", func(r *mutationRoutingRunner) { r.appMarker = "foreign" }},
		{"logical alias mismatch", path + ",4242,0", func(r *mutationRoutingRunner) { r.logicalPath = filepath.Join(t.TempDir(), "replacement.sock") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := &mutationRoutingRunner{logicalPath: path, logicalName: "rename-it"}
			tc.mutate(candidate)
			_, err := resolveExactObjectRuntimeMutationRoute(context.Background(), candidate, func(key string) string {
				if key == "TMUX" {
					return tc.tmux
				}
				return ""
			})
			if err == nil {
				t.Fatal("drifted app invocation acquired PID-only authority")
			}
			for _, call := range candidate.calls {
				if slices.Contains(tmuxCommandArgv(call[1:]), "set-option") {
					t.Fatalf("drifted route received a write: %#v", candidate.calls)
				}
			}
		})
	}
}

func TestWindowRenamePIDAuthorityStillRequiresExactUIDAndContainment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nondefault-app.sock")
	for _, tc := range []struct {
		name   string
		mutate func(*mutationRoutingRunner)
		want   string
	}{
		{"foreign Window UID", func(r *mutationRoutingRunner) { r.windowUID = "win-foreign" }, ""},
		{"foreign Project containment", func(r *mutationRoutingRunner) { r.projectUID = "prj-foreign" }, "Window Project containment drifted"},
		{"ControlSession role on Project Window", func(r *mutationRoutingRunner) { r.sessionRole = "control" }, "Window Project containment drifted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeResourceStore(t)
			runner := &mutationRoutingRunner{logicalPath: path, logicalName: "rename-it"}
			tc.mutate(runner)
			cmd := newTestRenameCommand(store)
			cmd.mirror = &fakeMutationMirror{windowTarget: "@7"}
			cmd.tmuxRunner = runner
			cmd.lookupEnv = func(key string) string {
				if key == "TMUX" {
					return path + ",4242,0"
				}
				return ""
			}

			_, _, err := runRoute(t, cmd, "window", "review", "--project", "alpha", "--name", "stable-window")
			if tc.want == "" {
				if err != nil {
					t.Fatalf("metadata-authoritative missing exact UID: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("containment error = %v, want %q", err, tc.want)
			}
			if runner.stableName == "stable-window" {
				t.Fatal("unattributed Window received the stable-name projection")
			}
			for _, call := range runner.calls {
				if slices.Contains(tmuxCommandArgv(call[1:]), "set-option") {
					t.Fatalf("unattributed Window reached a write: %#v", runner.calls)
				}
			}
		})
	}
}

func TestRenameAgentChangesOnlyStableName(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	agentBefore, _ := store.registry.Agent("agt-alpha-codex")
	paneBefore, _ := store.registry.Pane("pan-alpha-codex")
	mirror := &fakeMutationMirror{projectTarget: "$1", windowTarget: "@1", paneTarget: "%1"}
	cmd := newTestRenameCommand(store)
	cmd.mirror = mirror
	if _, stderr, err := runRoute(t, cmd, "agent", "codex", "--project", "alpha", "--window", "main", "--name", "reviewer"); err != nil {
		t.Fatalf("rename agent error = %v (stderr=%s)", err, stderr)
	}
	agentAfter, _ := store.registry.Agent("agt-alpha-codex")
	paneAfter, _ := store.registry.Pane("pan-alpha-codex")
	if agentAfter.Metadata.Name != "reviewer" {
		t.Fatalf("agent name = %q, want reviewer", agentAfter.Metadata.Name)
	}
	if agentAfter.Spec.Provider != agentBefore.Spec.Provider ||
		!reflect.DeepEqual(agentAfter.Metadata.Annotations, agentBefore.Metadata.Annotations) ||
		agentAfter.Status.PaneRef != agentBefore.Status.PaneRef {
		t.Fatalf("rename agent changed provider/topic/runtime linkage: before=%+v after=%+v", agentBefore, agentAfter)
	}
	if !reflect.DeepEqual(paneAfter, paneBefore) {
		t.Fatalf("rename agent changed managed Pane: before=%+v after=%+v", paneBefore, paneAfter)
	}
	if len(mirror.calls) != 0 {
		t.Fatalf("rename agent touched tmux: %v", mirror.calls)
	}
}

func TestOfflineAndUnavailableInventoryLeaveRetryableRegistryDrift(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		lookupErr error
	}{
		{name: "no exact live target"},
		{name: "inventory unavailable is not evidence of liveness", lookupErr: errors.New("no server running")},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			mirror := &fakeMutationMirror{lookupErr: test.lookupErr}
			cmd := newTestRenameCommand(store)
			cmd.mirror = mirror
			if _, stderr, err := runRoute(t, cmd, "project", "alpha", "--name", "offline-name"); err != nil {
				t.Fatalf("offline rename error = %v (stderr=%s)", err, stderr)
			}
			project, _ := store.registry.Project("prj-alpha")
			if project.Metadata.Name != "offline-name" || store.writes != 1 {
				t.Fatalf("offline Registry result = %+v writes=%d", project, store.writes)
			}
			if len(mirror.calls) != 1 || !strings.HasPrefix(mirror.calls[0], "find project") {
				t.Fatalf("offline mirror calls = %v", mirror.calls)
			}
		})
	}
}

func TestLiveMirrorFailureIsNonzeroAfterDurableRegistryCommit(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	mirror := &fakeMutationMirror{windowTarget: "@7", writeErr: mutationExitError{}}
	cmd := newTestRenameCommand(store)
	cmd.mirror = mirror
	stdout, _, err := runRoute(t, cmd, "window", "review", "--project", "alpha", "--name", "audit")
	if err == nil || IsUsageError(err) {
		t.Fatalf("live mirror failure error = %v, want runtime nonzero", err)
	}
	if stdout != "" {
		t.Fatalf("failure stdout = %q, want zero bytes", stdout)
	}
	if !strings.Contains(err.Error(), "committed Registry state") || !strings.Contains(err.Error(), "projmux reconcile resources") {
		t.Fatalf("failure error = %q, want durable result and retry", err)
	}
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		t.Fatalf("failure exposed subprocess ExitCode %d; cmd/projmux would suppress its recovery diagnostic", coded.ExitCode())
	}
	window, _ := store.registry.Window("win-alpha-review")
	if window.Metadata.Name != "audit" || store.writes != 1 {
		t.Fatalf("Registry did not retain retryable drift: window=%+v writes=%d", window, store.writes)
	}
}

func TestRebindImmediatelyConvergesOnlyProjectPathAndSurfacesFailure(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	newRoot := filepath.Join(root, "moved")
	if err := os.Mkdir(newRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	store := newFakeResourceStore(t)
	store.dirs[newRoot] = true
	mirror := &fakeMutationMirror{projectTarget: "$1"}
	cmd := newTestRebindCommand(store)
	cmd.mirror = mirror
	if _, stderr, err := runRoute(t, cmd, "project", "alpha", "--root", newRoot); err != nil {
		t.Fatalf("rebind error = %v (stderr=%s)", err, stderr)
	}
	if want := []string{"find project prj-alpha", "rebind project $1 " + newRoot}; !reflect.DeepEqual(mirror.calls, want) {
		t.Fatalf("mirror calls = %v, want %v", mirror.calls, want)
	}

	failedRoot := filepath.Join(root, "failed")
	if err := os.Mkdir(failedRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	store.dirs[failedRoot] = true
	mirror.calls = nil
	mirror.writeErr = errors.New("injected path write failure")
	_, _, err := runRoute(t, cmd, "project", "alpha", "--root", failedRoot)
	if err == nil || !strings.Contains(err.Error(), "projmux reconcile resources") {
		t.Fatalf("rebind failure = %v, want visible retry", err)
	}
	project, _ := store.registry.Project("prj-alpha")
	if project.Spec.Root != failedRoot {
		t.Fatalf("failed live mirror rolled back Registry root to %q", project.Spec.Root)
	}
}

func TestAmbiguousLiveUIDClaimFailsClosedAfterRegistryCommit(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	mirror := &fakeMutationMirror{lookupErr: intmetadata.ErrAmbiguousMirror}
	cmd := newTestRenameCommand(store)
	cmd.mirror = mirror
	_, _, err := runRoute(t, cmd, "pane", "log", "--project", "alpha", "--window", "main", "--name", "audit")
	if err == nil || !strings.Contains(err.Error(), "committed Registry state") {
		t.Fatalf("ambiguous claim error = %v", err)
	}
	pane, _ := store.registry.Pane("pan-alpha-log")
	if pane.Metadata.Name != "audit" {
		t.Fatalf("Registry result was not retained: %+v", pane)
	}
}

func TestImmediateMutationMirrorsUseOnlyTheInheritedAbsoluteSocketAndStableHandles(t *testing.T) {
	t.Parallel()

	socket := filepath.Join(t.TempDir(), "tmux.sock")
	lookupEnv := func(key string) string {
		switch key {
		case "TMUX":
			return socket + ",4242,0"
		case "TMUX_PANE":
			return "%7"
		}
		return ""
	}
	for _, test := range []struct {
		name       string
		run        func(*testing.T, *fakeResourceStore, resourceMutationMirror, *mutationRoutingRunner) error
		wantTarget string
	}{
		{
			name: "rename project uses session id",
			run: func(t *testing.T, store *fakeResourceStore, mirror resourceMutationMirror, _ *mutationRoutingRunner) error {
				cmd := newTestRenameCommand(store)
				cmd.mirror = mirror
				_, _, err := runRoute(t, cmd, "project", "alpha", "--name", "routed")
				return err
			},
			wantTarget: "-t $1",
		},
		{
			name: "rename window uses window id",
			run: func(t *testing.T, store *fakeResourceStore, mirror resourceMutationMirror, runner *mutationRoutingRunner) error {
				cmd := newTestRenameCommand(store)
				cmd.mirror = mirror
				cmd.tmuxRunner = runner
				cmd.lookupEnv = lookupEnv
				_, _, err := runRoute(t, cmd, "window", "review", "--project", "alpha", "--name", "routed")
				return err
			},
			wantTarget: "-t @7",
		},
		{
			name: "rename pane uses pane id",
			run: func(t *testing.T, store *fakeResourceStore, mirror resourceMutationMirror, _ *mutationRoutingRunner) error {
				cmd := newTestRenameCommand(store)
				cmd.mirror = mirror
				_, _, err := runRoute(t, cmd, "pane", "log", "--project", "alpha", "--window", "main", "--name", "routed")
				return err
			},
			wantTarget: "-t %7",
		},
		{
			name: "rebind project uses session id",
			run: func(t *testing.T, store *fakeResourceStore, mirror resourceMutationMirror, _ *mutationRoutingRunner) error {
				root := t.TempDir()
				store.dirs[root] = true
				cmd := newTestRebindCommand(store)
				cmd.mirror = mirror
				_, _, err := runRoute(t, cmd, "project", "alpha", "--root", root)
				return err
			},
			wantTarget: "-t $1",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := &mutationRoutingRunner{logicalPath: socket}
			mirror := inheritedResourceMutationMirror(lookupEnv, runner)
			if mirror == nil {
				t.Fatal("absolute inherited socket did not enable the immediate mirror")
			}
			if err := test.run(t, newFakeResourceStore(t), mirror, runner); err != nil {
				t.Fatal(err)
			}
			foundTarget := false
			for _, call := range runner.calls {
				if test.name == "rename window uses window id" && len(call) >= 3 && call[0] == "tmux" && call[1] == "-L" && call[2] == defaultAppSocket && slices.Contains(call, "#{socket_path}") {
					continue
				}
				if len(call) < 3 || call[0] != "tmux" || call[1] != "-S" || call[2] != socket {
					t.Fatalf("call was not exact-socket routed: %v", call)
				}
				if strings.Contains(strings.Join(call, " "), test.wantTarget) {
					foundTarget = true
				}
			}
			if !foundTarget {
				t.Fatalf("tmux calls = %v, want stable handle %q", runner.calls, test.wantTarget)
			}
		})
	}
}

func TestOutsideTmuxMutationIsRegistryOnlyAndNeverProbesDefaultServer(t *testing.T) {
	t.Parallel()

	for _, inherited := range []string{"", "relative-socket,4242,0"} {
		runner := &mutationRoutingRunner{}
		mirror := inheritedResourceMutationMirror(func(key string) string {
			if key == "TMUX" {
				return inherited
			}
			return ""
		}, runner)
		if mirror != nil {
			t.Fatalf("TMUX=%q enabled an immediate mirror", inherited)
		}

		store := newFakeResourceStore(t)
		cmd := newTestRenameCommand(store)
		cmd.mirror = mirror
		if _, _, err := runRoute(t, cmd, "project", "alpha", "--name", "registry-only"); err != nil {
			t.Fatalf("Registry-only rename: %v", err)
		}
		project, _ := store.registry.Project("prj-alpha")
		if project.Metadata.Name != "registry-only" || store.writes != 1 {
			t.Fatalf("Registry-only result = %+v writes=%d", project, store.writes)
		}
		if len(runner.calls) != 0 {
			t.Fatalf("outside-tmux rename probed a default server: %v", runner.calls)
		}
	}
}

var _ resourceMutationMirror = (*fakeMutationMirror)(nil)
