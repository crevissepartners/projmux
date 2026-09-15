package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// cliWindowRenameRealTmuxEnv makes tmux mandatory for the public `rename
// window` display boundary. test/integration/cli-window-rename-display.sh sets
// it so a missing tmux fails the integration suite instead of skipping, which
// is what keeps this assertion alive in CI: the Unit job has no tmux.
const cliWindowRenameRealTmuxEnv = "PMX_TEST_CLI_WINDOW_RENAME_REAL_TMUX"

// TestCLIWindowRenameConvergesTheTabThroughRealTmux drives the public
// `projmux rename window` route against an isolated real tmux server, wired the
// way newRenameCommand wires it. Inside the runtime the Registry name,
// @projmux_window_name and the tab #{window_name} must end the command as one
// value, including for names spelled like tmux flags, and re-running the same
// name must repair a tab someone left stale -- the Recovery the keybinding
// rename contract points at.
func TestCLIWindowRenameConvergesTheTabThroughRealTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		if os.Getenv(cliWindowRenameRealTmuxEnv) == "1" {
			t.Fatalf("%s=1 requires tmux: %v", cliWindowRenameRealTmuxEnv, err)
		}
		t.Skip("tmux is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	root, err := os.MkdirTemp("", "pcr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if root, err = filepath.EvalSymlinks(root); err != nil {
		t.Fatal(err)
	}
	environment := []string{"TMUX_TMPDIR=" + root}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "TMUX" || key == "TMUX_PANE" || key == "TMUX_TMPDIR" || key == runtimeMutationAnchorPaneEnv {
			continue
		}
		environment = append(environment, entry)
	}
	// The app route re-resolves `-L <logical>` and requires the same socket
	// path, so the server listens where TMUX_TMPDIR places that logical name.
	const logical = "pcr"
	socketDir := filepath.Join(root, fmt.Sprintf("tmux-%d", os.Getuid()))
	if err := os.Mkdir(socketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(socketDir, logical)
	projectRoot := filepath.Join(root, "project")
	if err := os.Mkdir(projectRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	tmux := func(args ...string) (string, error) {
		command := exec.CommandContext(ctx, "tmux", append([]string{"-S", socket, "-f", "/dev/null"}, args...)...)
		command.Env = environment
		out, err := command.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	const sessionName, projectUID, windowUID, paneUID = "cli-rename", "prj-cli-rename", "win-cli-rename", "pan-cli-rename"
	created, err := tmux("new-session", "-d", "-s", sessionName, "-n", "main", "-c", projectRoot,
		"-P", "-F", "#{session_id}\t#{window_id}\t#{pane_id}\t#{pid}\t#{socket_path}", "tail", "-f", "/dev/null")
	if err != nil {
		t.Fatalf("start isolated tmux: %v: %s", err, created)
	}
	fields := strings.Split(created, "\t")
	var sessionID, windowID, paneID, serverPID string
	if len(fields) == 5 {
		sessionID, windowID, paneID, serverPID = fields[0], fields[1], fields[2], fields[3]
	}
	// Registered before the receipt is judged, so no t.Fatalf below leaves the
	// server running, and after the root removal above, so LIFO kills it while
	// its socket still exists.
	killRealTmuxServerOnCleanup(t, environment, socket, realTmuxServerPID(t, serverPID))
	if len(fields) != 5 || exactTmuxHandle(sessionID, "$") == "" || exactTmuxHandle(windowID, "@") == "" ||
		exactTmuxHandle(paneID, "%") == "" || fields[4] != socket {
		t.Fatalf("isolated tmux receipt = %q, want session/window/pane/pid on %s", created, socket)
	}
	for _, option := range [][]string{
		{"-g", tmuxopts.AppGlobal, "1"},
		{"-g", runtimeMutationSocketNameOption, logical},
		{"-t", sessionID, tmuxopts.ProjectUIDSession, projectUID},
		{"-t", sessionID, tmuxopts.ProjectPathSession, projectRoot},
		{"-w", "-t", windowID, tmuxopts.AutomaticRenameWindow, "off"},
		{"-w", "-t", windowID, tmuxopts.WindowUID, windowUID},
		{"-w", "-t", windowID, tmuxopts.WindowName, "main"},
		{"-p", "-t", paneID, tmuxopts.PaneUID, paneUID},
	} {
		if out, err := tmux(append([]string{"set-option"}, option...)...); err != nil {
			t.Fatalf("seed isolated tmux %q: %v: %s", option, err, out)
		}
	}

	ownedBy := func(kind coremetadata.Kind, uid string) *coremetadata.OwnerRef {
		return &coremetadata.OwnerRef{Kind: kind, UID: uid}
	}
	registry := coremetadata.NewRegistry()
	registry.Projects = []coremetadata.Project{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindProject,
		Metadata: coremetadata.ObjectMeta{UID: projectUID, Name: "cli-rename", CreatedAt: resourceFixtureClock},
		Spec:     coremetadata.ProjectSpec{Root: projectRoot, PrimaryWindowRef: windowUID},
		Status:   coremetadata.ProjectStatus{Session: &coremetadata.SessionProjection{Name: sessionName, Live: true}},
	}}
	registry.Windows = []coremetadata.Window{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
		Metadata: coremetadata.ObjectMeta{UID: windowUID, Name: "main", OwnerRef: ownedBy(coremetadata.KindProject, projectUID), CreatedAt: resourceFixtureClock},
		Spec:     coremetadata.WindowSpec{AnchorPaneRef: paneUID},
	}}
	registry.Panes = []coremetadata.Pane{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
		Metadata: coremetadata.ObjectMeta{UID: paneUID, Name: "shell", OwnerRef: ownedBy(coremetadata.KindWindow, windowUID), CreatedAt: resourceFixtureClock},
		Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell, CWD: projectRoot},
	}}
	registry.NameReservations = []coremetadata.NameReservation{
		{Scope: "", Kind: coremetadata.KindProject, Name: "cli-rename", UID: projectUID},
		{Scope: projectUID, Kind: coremetadata.KindWindow, Name: "main", UID: windowUID},
		{Scope: projectUID, Kind: coremetadata.KindPane, Name: "shell", UID: paneUID},
	}
	registry = registry.Normalize()
	if err := registry.Validate(); err != nil {
		t.Fatalf("cli-rename fixture is not a valid Registry: %v", err)
	}
	store := &fakeResourceStore{registry: registry, dirs: map[string]bool{projectRoot: true}, now: resourceFixtureClock}

	// The operator's shell inside the Project Pane inherits exactly this.
	inherited := map[string]string{"TMUX": socket + "," + serverPID + ",0", "TMUX_PANE": paneID}
	lookupEnv := func(key string) string { return inherited[key] }
	runner := shellTmuxExecRunner{env: func() []string { return environment }}
	newRename := func() *renameCommand {
		return &renameCommand{
			store:      store.store(),
			mirror:     inheritedResourceMutationMirror(lookupEnv, runner),
			runtime:    fixedRuntimeLookup([]string{windowUID}, []string{paneUID}),
			tmuxRunner: runner,
			lookupEnv:  lookupEnv,
		}
	}
	read := func(t *testing.T, format string) string {
		t.Helper()
		got, err := tmux("display-message", "-p", "-t", windowID, "-F", format)
		if err != nil {
			t.Fatalf("read %s: %v: %s", format, err, got)
		}
		return got
	}
	rename := func(t *testing.T, name string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if err := newRename().Run([]string{"window", "uid:" + windowUID, "--project", "uid:" + projectUID, "--name", name}, &stdout, &stderr); err != nil {
			t.Fatalf("rename Window to %q: %v (stdout=%q stderr=%q)", name, err, stdout.String(), stderr.String())
		}
		if stderr.String() != "" {
			t.Fatalf("rename to %q inside the runtime wrote a display notice: %q", name, stderr.String())
		}
		stored, ok := store.registry.Window(windowUID)
		if !ok || stored.Metadata.Name != name {
			t.Fatalf("Registry Window %s name = %q, want %q", windowUID, stored.Metadata.Name, name)
		}
		for _, format := range []string{"#{window_name}", "#{" + tmuxopts.WindowName + "}"} {
			if got := read(t, format); got != name {
				t.Fatalf("Window %s %s = %q, want %q", windowID, format, got, name)
			}
		}
	}

	// Ordinary and flag-shaped names: tmux reads a leading-dash new-name as a
	// flag unless options end first, so each one proves the canonical argv.
	for _, name := range []string{"review", "-L", "-s", "-Lx", "-tfoo"} {
		t.Run(name, func(t *testing.T) { rename(t, name) })
	}

	// Recovery: a tab left stale beside an already-matching stable name is
	// repaired by re-running the same rename.
	t.Run("stale tab recovery", func(t *testing.T) {
		if out, err := tmux("rename-window", "-t", windowID, "--", "stale-tab"); err != nil {
			t.Fatalf("stale the tab: %v: %s", err, out)
		}
		current, ok := store.registry.Window(windowUID)
		if !ok {
			t.Fatal("fixture Window disappeared")
		}
		if got := read(t, "#{"+tmuxopts.WindowName+"}"); got != current.Metadata.Name {
			t.Fatalf("stable-name mirror = %q, want the unchanged Registry name %q", got, current.Metadata.Name)
		}
		rename(t, current.Metadata.Name)
	})
}
