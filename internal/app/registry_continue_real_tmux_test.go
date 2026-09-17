package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// registryContinueRealTmuxEnv makes tmux mandatory for the Registry Continue
// fidelity check. The L05 E2E scenario sets it, so a missing tmux fails the
// scenario instead of skipping it; the Unit job has no tmux and skips.
const registryContinueRealTmuxEnv = "PROJMUX_REAL_TMUX_TEST"

// registryContinueLiveRow is one Pane of the observed runtime, in tmux order.
type registryContinueLiveRow struct {
	windowUID, windowName, windowMirror, paneUID, cwd string
}

func (r registryContinueLiveRow) String() string {
	return fmt.Sprintf("window=%s name=%s mirror=%s pane=%s cwd=%s", r.windowUID, r.windowName, r.windowMirror, r.paneUID, r.cwd)
}

// TestRealTmuxRegistryContinueFieldFidelity is the L05 persistence check. The
// Registry is the only saved Project state, so it proves that state survives a
// runtime loss on a real tmux server:
//
//  1. a registered Project declares two named Windows with named shell Panes in
//     distinct directories;
//  2. Continue project materializes that desired state on an isolated server;
//  3. the Project session is killed (a keeper session keeps the server alive)
//     and the window and pane index bases change, so Continue cannot lean on
//     index-derived targets;
//  4. Continue project runs again.
//
// After both Continues the live Windows and Panes carry exactly the Registry
// UIDs, names, and working directories in Registry order, and the second
// Continue never changes a Registry UID, name, or directory.
func TestRealTmuxRegistryContinueFieldFidelity(t *testing.T) {
	if os.Getenv(registryContinueRealTmuxEnv) == "" {
		t.Skipf("set %s=1 to run disposable real-tmux Continue coverage", registryContinueRealTmuxEnv)
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Fatalf("%s requires tmux: %v", registryContinueRealTmuxEnv, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	root, err := os.MkdirTemp("", "prc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if root, err = filepath.EvalSymlinks(root); err != nil {
		t.Fatal(err)
	}
	// The server and its shells get an empty HOME and a plain POSIX shell, so
	// no operator rc file, history, or hook configuration takes part.
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	environment := []string{"TMUX_TMPDIR=" + root, "HOME=" + home, "SHELL=/bin/sh", "ENV=", "XDG_CONFIG_HOME=" + filepath.Join(home, ".config"), "XDG_STATE_HOME=" + filepath.Join(home, ".local", "state")}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "TMUX", "TMUX_PANE", "TMUX_TMPDIR", "HOME", "SHELL", "ENV", "XDG_CONFIG_HOME", "XDG_STATE_HOME", runtimeMutationAnchorPaneEnv:
			continue
		}
		environment = append(environment, entry)
	}
	// `-L <logical>` resolves below TMUX_TMPDIR, so the materializer's exact
	// target and the direct `-S` probes below name the same isolated server.
	const logical = "prc"
	socketDir := filepath.Join(root, fmt.Sprintf("tmux-%d", os.Getuid()))
	if err := os.Mkdir(socketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(socketDir, logical)
	projectRoot := filepath.Join(root, "project")
	logsDir := filepath.Join(projectRoot, "logs")
	docsDir := filepath.Join(projectRoot, "docs")
	for _, dir := range []string{projectRoot, logsDir, docsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tmux := func(args ...string) (string, error) {
		command := exec.CommandContext(ctx, "tmux", append([]string{"-S", socket, "-f", "/dev/null"}, args...)...)
		command.Env = environment
		out, err := command.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}

	// A keeper session holds the server open across the Project session kill.
	created, err := tmux("new-session", "-d", "-s", "prc-keeper", "-c", root, "-P", "-F", "#{pid}\t#{socket_path}")
	if err != nil {
		t.Fatalf("start isolated tmux: %v: %s", err, created)
	}
	fields := strings.Split(created, "\t")
	pidField := ""
	if len(fields) == 2 {
		pidField = fields[0]
	}
	killRealTmuxServerOnCleanup(t, environment, socket, realTmuxServerPID(t, pidField))
	if len(fields) != 2 || fields[1] != socket {
		t.Fatalf("isolated tmux receipt = %q, want pid and %s", created, socket)
	}
	// The same ownership markers `config apply` writes on the app server, so
	// Continue binds the exact app-owned route it binds in production.
	for _, option := range [][]string{
		{"-g", tmuxopts.AppGlobal, "1"},
		{"-g", runtimeMutationSocketNameOption, logical},
	} {
		if out, err := tmux(append([]string{"set-option"}, option...)...); err != nil {
			t.Fatalf("mark isolated tmux %q: %v: %s", option, err, out)
		}
	}

	// Registry desired state: two named Windows, three named shell Panes.
	const sessionName = "prc-project"
	store := &fakeResourceStore{
		registry: coremetadata.NewRegistry(),
		dirs:     map[string]bool{projectRoot: true, logsDir: true, docsDir: true},
		now:      resourceFixtureClock,
	}
	registered, err := store.mutator().RegisterProject(&store.registry, coremetadata.RegisterProjectOptions{
		Root: projectRoot, Name: "fidelity", SessionName: sessionName, DefaultShell: "/bin/sh",
		Topology: []coremetadata.BootstrapWindow{
			{Name: "editor", Panes: []coremetadata.BootstrapPane{
				{Name: "code", CWD: projectRoot},
				{Name: "tail", CWD: logsDir},
			}},
			{Name: "notes", Panes: []coremetadata.BootstrapPane{
				{Name: "writing", CWD: docsDir},
			}},
		},
		OperationID: "op-l05-register",
	})
	if err != nil {
		t.Fatalf("register fidelity Project: %v", err)
	}
	projectUID := registered.Project.Metadata.UID
	if err := store.registry.Validate(); err != nil {
		t.Fatalf("fidelity Registry is invalid: %v", err)
	}

	desired := func(t *testing.T) []registryContinueLiveRow {
		t.Helper()
		var rows []registryContinueLiveRow
		for _, window := range store.registry.WindowsOf(projectUID) {
			for _, pane := range store.registry.PanesOf(window.Metadata.UID) {
				rows = append(rows, registryContinueLiveRow{
					windowUID: window.Metadata.UID, windowName: window.Metadata.Name, windowMirror: window.Metadata.Name,
					paneUID: pane.Metadata.UID, cwd: pane.Spec.CWD,
				})
			}
		}
		return rows
	}
	want := desired(t)
	if len(want) != 3 || want[0].windowName != "editor" || want[2].windowName != "notes" {
		t.Fatalf("fidelity desired rows = %v, want editor(code, tail) then notes(writing)", want)
	}

	runner := shellTmuxExecRunner{env: func() []string { return environment }}
	target, err := tmuxSocketNameTarget(logical)
	if err != nil {
		t.Fatal(err)
	}
	notices := &bytes.Buffer{}
	activation := &registryProjectTopologyMaterializer{
		resources:      store.store(),
		runner:         runner,
		target:         target,
		newReconciler:  reconcileFixtureReconciler(projectRoot, sessionName),
		newOperationID: func() (string, error) { return "op-l05-continue", nil },
		// No lifecycle hooks: the isolated run must never read the operator's
		// hook configuration.
		newMaterializer: func(exact tmuxCommandRunner, warn io.Writer) *materializer {
			client := inttmux.NewClient(exact, inttmux.WithSocketName(logical))
			return &materializer{runner: exact, mirror: intmetadata.NewMirror(exact), sessions: client, target: target, warn: warn}
		},
		warn:    io.Discard,
		agents:  newFakeTopologyAgentLauncher(),
		notices: notices,
	}
	activation.resolveRoute = func(ctx context.Context, anchor string) (runtimeMutationRoute, error) {
		return resolveExistingRuntimeMutationRouteWithAnchor(ctx, runner, target, func(string) string { return "" }, anchor)
	}
	reporter := &recordingProjectStartupReporter{}
	switcher := &switchCommand{projectTopology: activation, startupNotices: reporter}
	continueProject := func(t *testing.T, label string) {
		t.Helper()
		started := time.Now()
		defer func() { t.Logf("%s Continue took %s", label, time.Since(started)) }()
		project, ok := store.registry.Project(projectUID)
		if !ok {
			t.Fatalf("%s: Project %s disappeared", label, projectUID)
		}
		err := switcher.continueProjectOpenRequest(ctx, projectOpenRequest{
			Target: projectRoot, SessionName: sessionName,
			Mode:     projectStartupCandidate{Kind: projectStartupKindTopology},
			Detached: true,
		}, openedProjectBootstrap{project: project.Clone(), materializeTopology: true})
		if err != nil {
			t.Fatalf("%s Continue project: %v\nnotices:\n%s", label, err, notices.String())
		}
	}
	observe := func(t *testing.T, label string) []registryContinueLiveRow {
		t.Helper()
		out, err := tmux("list-panes", "-s", "-t", "="+sessionName, "-F", strings.Join([]string{
			"#{" + tmuxopts.WindowUID + "}", "#{window_name}", "#{" + tmuxopts.WindowName + "}",
			"#{" + tmuxopts.PaneUID + "}", "#{pane_current_path}",
		}, "\t"))
		if err != nil {
			t.Fatalf("%s: list live Panes: %v: %s", label, err, out)
		}
		var rows []registryContinueLiveRow
		for line := range strings.SplitSeq(out, "\n") {
			parts := strings.Split(line, "\t")
			if len(parts) != 5 {
				t.Fatalf("%s: live Pane row %q", label, line)
			}
			rows = append(rows, registryContinueLiveRow{
				windowUID: parts[0], windowName: parts[1], windowMirror: parts[2], paneUID: parts[3], cwd: parts[4],
			})
		}
		if got, err := tmux("display-message", "-p", "-t", "="+sessionName+":", "#{"+tmuxopts.ProjectUIDSession+"}"); err != nil || got != projectUID {
			t.Fatalf("%s: session Project UID = %q (%v), want %q", label, got, err, projectUID)
		}
		return rows
	}
	assertFidelity := func(t *testing.T, label string, got []registryContinueLiveRow) {
		t.Helper()
		if !slices.Equal(got, want) {
			t.Fatalf("%s live topology differs from the Registry\ngot:\n%s\nwant:\n%s", label, joinContinueRows(got), joinContinueRows(want))
		}
	}

	continueProject(t, "first")
	assertFidelity(t, "first Continue", observe(t, "first Continue"))
	if current := desired(t); !slices.Equal(current, want) {
		t.Fatalf("first Continue changed Registry-owned fields\ngot:\n%s\nwant:\n%s", joinContinueRows(current), joinContinueRows(want))
	}

	// Lose the runtime, and move the index bases so any index-derived target
	// would now name the wrong Window or Pane.
	for _, args := range [][]string{
		{"kill-session", "-t", "=" + sessionName},
		{"set-option", "-g", "base-index", "3"},
		{"set-option", "-g", "pane-base-index", "5"},
	} {
		if out, err := tmux(args...); err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
	}
	if out, err := tmux("has-session", "-t", "="+sessionName); err == nil {
		t.Fatalf("Project session survived kill-session: %s", out)
	}

	continueProject(t, "second")
	assertFidelity(t, "Continue after runtime loss", observe(t, "Continue after runtime loss"))
	if current := desired(t); !slices.Equal(current, want) {
		t.Fatalf("Continue after runtime loss changed Registry-owned fields\ngot:\n%s\nwant:\n%s", joinContinueRows(current), joinContinueRows(want))
	}
	if project, ok := store.registry.Project(projectUID); !ok || project.Spec.Root != projectRoot || project.Metadata.Name != "fidelity" {
		t.Fatalf("Continue after runtime loss changed the Project identity: %+v", project)
	}

	// A repeat Continue on the live runtime is a Registry and topology no-op.
	writes := store.writes
	before := observe(t, "before repeat")
	continueProject(t, "repeat")
	if store.writes != writes {
		t.Fatalf("repeat Continue wrote the Registry: %d -> %d", writes, store.writes)
	}
	if after := observe(t, "after repeat"); !slices.Equal(after, before) {
		t.Fatalf("repeat Continue changed the live topology\nbefore:\n%s\nafter:\n%s", joinContinueRows(before), joinContinueRows(after))
	}
}

func joinContinueRows(rows []registryContinueLiveRow) string {
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		lines = append(lines, row.String())
	}
	return strings.Join(lines, "\n")
}
