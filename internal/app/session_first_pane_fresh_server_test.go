package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	intmux "github.com/crevissepartners/projmux/internal/integrations/mux"
)

// ensureSessionAt is ensureSessionLaunching with no first-Pane activation,
// the shape the session-creation tests that predate it exercise.
func (m *materializer) ensureSessionAt(
	ctx context.Context,
	project coremetadata.Project,
	sessionName, runtimeCWD, firstWindowName string,
	ledger *runtimeLedger,
) (intmux.NewSessionResult, error) {
	result, _, err := m.ensureSessionLaunching(ctx, project, sessionName, runtimeCWD, firstWindowName, nil, ledger)
	return result, err
}

// TestFreshServerSessionFirstPaneResolvesItsDefaultsInsideTheServer covers the
// one case the ordinary launch cannot: no server exists yet, so tmux's
// default-shell and default-command cannot be read before new-session. The
// first Pane is still launched under its own generation, and the supervisor
// reads those defaults from the server it runs in.
func TestFreshServerSessionFirstPaneResolvesItsDefaultsInsideTheServer(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "projmux", "tmux.conf")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("set-option -g @projmux_app 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := newFakeTmux()
	server.serverAbsent = true
	server.appMarker = "1"
	server.socketName = ""
	target := tmuxTransport{Kind: tmuxSocketName, Value: "first-pane-fresh", Source: tmuxSocketNameSource}
	routed := explicitTmuxRunner{runner: server, target: target}
	runtime := &materializer{
		runner: routed, mirror: intmetadata.NewMirror(routed), sessions: &fakeSessionMaterializer{tmux: server},
		target: target, configPath: configPath, executable: func() (string, error) { return testSupervisorBinary, nil },
	}
	project := coremetadata.Project{
		Metadata: coremetadata.ObjectMeta{UID: "prj-fresh", Name: "fresh"},
		Spec:     coremetadata.ProjectSpec{Root: "/work/fresh"},
	}
	spec := superviseSpec{PaneUID: "pan-fresh-first", Generation: "gen-fresh-first", OperationID: "op-fresh"}
	issued := 0
	result, launched, err := runtime.ensureSessionLaunching(context.Background(), project, "fresh", project.Spec.Root, "w-first",
		func() (superviseSpec, error) { issued++; return spec, nil }, newRuntimeLedger("op-fresh"))
	if err != nil {
		t.Fatalf("fresh Project materialize: %v", err)
	}
	if issued != 1 || launched != spec || !result.Created {
		t.Fatalf("fresh first Pane activation issued=%d launched=%+v created=%t", issued, launched, result.Created)
	}
	var argv []string
	for _, call := range server.calls {
		if candidate := tmuxCommandArgv(call); slices.Contains(candidate, "new-session") {
			argv = candidate
		}
	}
	if len(argv) < 3 || argv[2] != "new-session" {
		t.Fatalf("fresh new-session argv = %#v, want -f config before new-session", argv)
	}
	want := []string{testSupervisorBinary, "internal", "supervise", "--pane-uid", spec.PaneUID, "--generation", spec.Generation,
		"--operation-id", spec.OperationID, "--" + superviseServerDefaultCommandFlag, "--"}
	if !slices.Equal(trailingCommand(argv[2:]), want) {
		t.Fatalf("fresh new-session launch = %#v, want %#v (full argv %#v)", trailingCommand(argv[2:]), want, argv)
	}
	for _, call := range server.calls {
		if slices.Contains(call, "default-shell") || slices.Contains(call, "default-command") {
			t.Fatalf("fresh create read pane defaults from a server that did not exist yet: %#v", call)
		}
	}

	// A session that is already live is reused, and its first Pane keeps the
	// activation it already runs under: nothing is issued for it.
	issued = 0
	again, launched, err := runtime.ensureSessionLaunching(context.Background(), project, "fresh", project.Spec.Root, "w-first",
		func() (superviseSpec, error) { issued++; return spec, nil }, newRuntimeLedger("op-again"))
	if err != nil || again.Created || issued != 0 || launched.valid() {
		t.Fatalf("live session reuse = created %t issued %d launched %+v err %v", again.Created, issued, launched, err)
	}
}

// TestSuperviseResolvesTheServerDefaultCommandItself pins the supervisor half
// of the fresh-server first Pane: with no child argv it runs exactly the
// process tmux would have started, read from its own server.
func TestSuperviseResolvesTheServerDefaultCommandItself(t *testing.T) {
	for _, test := range []struct {
		name, shell, command string
		wantArgv0            string
		wantChild            []string
	}{
		{name: "login shell", shell: "/usr/bin/zsh", wantArgv0: "-zsh", wantChild: []string{"/usr/bin/zsh"}},
		{name: "default-command", shell: "/bin/bash", command: "exec fish", wantArgv0: "bash", wantChild: []string{"/bin/bash", "-c", "exec fish"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newFakeResourceStore(t)
			cmd, started := newTestSuperviseCommand(t, store, processOutcome{}, nil)
			cmd.serverPaneDefaults = func() (string, string, error) { return test.shell, test.command, nil }
			err := cmd.Run([]string{"--pane-uid", "pan-alpha-log", "--generation", "gen-1", "--" + superviseServerDefaultCommandFlag, "--"}, nil, &bytes.Buffer{})
			if code, ok := err.(superviseExitError); !ok || code.code != 0 {
				t.Fatalf("supervise = %v", err)
			}
			want := strings.Join(append([]string{test.wantArgv0}, test.wantChild...), " ")
			if len(*started) != 1 || (*started)[0] != want {
				t.Fatalf("started %q, want %q", *started, want)
			}
		})
	}

	store := newFakeResourceStore(t)
	cmd, started := newTestSuperviseCommand(t, store, processOutcome{}, nil)
	cmd.serverPaneDefaults = func() (string, string, error) { return "/bin/zsh", "", nil }
	err := cmd.Run([]string{"--pane-uid", "pan-alpha-log", "--generation", "gen-1", "--" + superviseServerDefaultCommandFlag, "--", "sh"}, nil, &bytes.Buffer{})
	if err == nil || !IsUsageError(err) || len(*started) != 0 {
		t.Fatalf("server-default launch with an explicit child = %v started=%q, want a usage refusal", err, *started)
	}
}
