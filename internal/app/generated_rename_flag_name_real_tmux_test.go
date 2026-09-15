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
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// TestGeneratedWindowRenameProjectsFlagShapedNamesThroughRealTmux drives the
// production Window rename behind `internal tmux window-rename` (the rename key
// and the Window menu Rename item), renameWindowFromIntent, on an isolated real
// tmux server. The create and rename commands are wired the way
// newCreateCommand and newRenameCommand wire them, over an exec runner confined
// to that server and an in-memory Registry. tmux reads a leading-dash new-name
// as a flag unless options end first, so every flag-shaped name must reach
// #{window_name} through `rename-window -t @N -- <name>` and agree with
// @projmux_window_name and the Registry name.
func TestGeneratedWindowRenameProjectsFlagShapedNamesThroughRealTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		if os.Getenv(flagShapedWindowNameRealTmuxEnv) == "1" {
			t.Fatalf("%s=1 requires tmux: %v", flagShapedWindowNameRealTmuxEnv, err)
		}
		t.Skip("tmux is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	root, err := os.MkdirTemp("", "pfr-")
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
	const logical = "pfr"
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
	const sessionName, projectUID, windowUID, paneUID = "flag-rename", "prj-flag-rename", "win-flag-rename", "pan-flag-rename"
	created, err := tmux("new-session", "-d", "-s", sessionName, "-n", "main", "-c", projectRoot,
		"-P", "-F", "#{session_id}\t#{window_id}\t#{pane_id}\t#{pid}\t#{socket_path}", "tail", "-f", "/dev/null")
	if err != nil {
		t.Fatalf("start isolated tmux: %v: %s", err, created)
	}
	t.Cleanup(func() { _, _ = tmux("kill-server") })
	fields := strings.Split(created, "\t")
	if len(fields) != 5 || exactTmuxHandle(fields[0], "$") == "" || exactTmuxHandle(fields[1], "@") == "" ||
		exactTmuxHandle(fields[2], "%") == "" || fields[4] != socket {
		t.Fatalf("isolated tmux receipt = %q, want session/window/pane/pid on %s", created, socket)
	}
	sessionID, windowID, paneID, serverPID := fields[0], fields[1], fields[2], fields[3]
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
		Metadata: coremetadata.ObjectMeta{UID: projectUID, Name: "flag-rename", CreatedAt: resourceFixtureClock},
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
		{Scope: "", Kind: coremetadata.KindProject, Name: "flag-rename", UID: projectUID},
		{Scope: projectUID, Kind: coremetadata.KindWindow, Name: "main", UID: windowUID},
		{Scope: projectUID, Kind: coremetadata.KindPane, Name: "shell", UID: paneUID},
	}
	registry = registry.Normalize()
	if err := registry.Validate(); err != nil {
		t.Fatalf("flag-rename fixture is not a valid Registry: %v", err)
	}
	store := &fakeResourceStore{registry: registry, dirs: map[string]bool{projectRoot: true}, now: resourceFixtureClock}

	// The run-shell job the generated binding starts inherits TMUX and
	// TMUX_PANE from the exact client Pane; nothing else reaches the route.
	inherited := map[string]string{"TMUX": socket + "," + serverPID + ",0", "TMUX_PANE": paneID}
	lookupEnv := func(key string) string { return inherited[key] }
	runner := shellTmuxExecRunner{env: func() []string { return environment }}
	physical := explicitTmuxRunner{runner: runner, target: tmuxTransport{Kind: tmuxSocketPath, Value: socket, Source: tmuxSocketPathSource}}
	newCreate := func() *createCommand {
		target := tmuxTransport{Kind: tmuxSocketName, Value: logical, Source: tmuxSocketNameSource}
		routed := explicitTmuxRunner{runner: runner, target: target}
		client := defaultTmuxClientWithRunner(routed)
		command := &createCommand{
			store:      store.store(),
			reconciler: newRegistryReconciler(routed, client),
			runtime: &materializer{
				runner: routed, mirror: intmetadata.NewMirror(routed), sessions: client, target: target,
				warn: testWarnWriter{t}, executable: os.Executable, lookupEnv: lookupEnv,
			},
			anchorTarget: func(anchor string) activeTargetLookup {
				return anchoredActiveTargetLookup(anchor, lookupEnv, intmetadata.NewMirror(physical))
			},
			shell:          "/bin/sh",
			sessionNameFor: filepath.Base,
			newOperationID: newCreateOperationID,
			now:            time.Now,
			newGeneration:  coremetadata.NewGeneration,
		}
		bind := func(ctx context.Context, explicit bool) error {
			route, err := resolveInvocationRuntimeMutationRouteWithPolicy(ctx, runner, lookupEnv, command.routeAnchor, explicit)
			if err != nil {
				return err
			}
			exact := explicitTmuxRunner{runner: runner, target: route.target}
			client := defaultTmuxClientWithSocketRunner(exact, route.socketName)
			command.reconciler = newRegistryReconcilerWithRoute(exact, client, route)
			// Keep the reconciler off the real $HOME Project discovery.
			command.reconciler.discoverRoots = func() ([]string, error) { return nil, nil }
			command.runtime.runner = exact
			command.runtime.mirror = intmetadata.NewMirror(exact)
			command.runtime.sessions = client
			command.runtime.target = route.target
			command.runtime.expectedSocketPath = route.expectedSocketPath
			command.runtime.socketName = route.socketName
			command.runtime.routeAuthority = route.authority
			return nil
		}
		command.bindRuntime = func(ctx context.Context) error { return bind(ctx, false) }
		command.bindExplicitRuntime = func(ctx context.Context) error { return bind(ctx, true) }
		return command
	}
	renamer := &renameCommand{
		store:      store.store(),
		mirror:     inheritedResourceMutationMirror(lookupEnv, runner),
		tmuxRunner: runner,
		lookupEnv:  lookupEnv,
	}

	for _, name := range []string{"-L", "-t", "-s", "-Lx", "-S", "-Sx", "-sfoo", "-tfoo", "-f", "-n"} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			intent := windowRenameIntent{anchorPaneID: paneID, response: name}
			if err := newCreate().renameWindowFromIntent(intent, renamer, &stdout, &stderr); err != nil {
				t.Fatalf("rename Window to %q: %v (stderr=%q)", name, err, stderr.String())
			}
			if want := "renamed: window/" + windowUID + " -> " + name + "\n"; stdout.String() != want {
				t.Fatalf("stdout = %q, want %q", stdout.String(), want)
			}
			stored, ok := store.registry.Window(windowUID)
			if !ok || stored.Metadata.Name != name {
				t.Fatalf("Registry Window %s name = %q, want %q", windowUID, stored.Metadata.Name, name)
			}
			for _, format := range []string{"#{window_name}", "#{" + tmuxopts.WindowName + "}"} {
				got, err := tmux("display-message", "-p", "-t", windowID, "-F", format)
				if err != nil || got != name {
					t.Fatalf("Window %s %s = %q (err %v), want %q", windowID, format, got, err, name)
				}
			}
		})
	}
}
