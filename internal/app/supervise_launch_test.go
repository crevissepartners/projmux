package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// stubDefaultsRunner answers the two global option reads the shell-pane launch
// resolution makes and refuses everything else.
type stubDefaultsRunner struct {
	shell   string
	command string
	err     error
	calls   [][]string
}

func TestAgentLaunchCarriesTheCreatorResolvedAbsoluteRegistryPath(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_STATE_HOME", "relative state with spaces")

	spec := superviseSpec{
		PaneUID: "pan-alpha-codex", AgentUID: "agt-alpha-codex",
		Generation: "gen-exact", OperationID: "op-exact",
	}
	launch := newLaunchMaterializer(&stubDefaultsRunner{shell: "/bin/sh"}, &strings.Builder{}).
		supervisedLaunch(context.Background(), spec, []string{"provider"})
	stateDir, err := filepath.Abs(filepath.Join("relative state with spaces", "projmux"))
	if err != nil {
		t.Fatal(err)
	}
	wantPath := intmetadata.PathFor(stateDir)
	var gotPath string
	for i := range launch {
		if launch[i] == "--registry-path" && i+1 < len(launch) {
			gotPath = launch[i+1]
		}
	}
	if gotPath != wantPath || !filepath.IsAbs(gotPath) || filepath.Clean(gotPath) != gotPath {
		t.Fatalf("creator Registry path = %q, want exact clean absolute %q; launch=%v", gotPath, wantPath, launch)
	}
}

func (r *stubDefaultsRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	if r.err != nil {
		return nil, r.err
	}
	switch strings.Join(args, " ") {
	case "show-options -gv default-shell":
		return []byte(r.shell + "\n"), nil
	case "show-options -gv default-command":
		return []byte(r.command + "\n"), nil
	}
	return nil, errors.New("unexpected tmux call")
}

func newLaunchMaterializer(runner tmuxCommandRunner, warn *strings.Builder) *materializer {
	return &materializer{
		runner:     runner,
		warn:       warn,
		executable: func() (string, error) { return testSupervisorBinary, nil },
		lookupEnv:  func(string) string { return "" },
	}
}

// TestShellPaneLaunchReproducesTmuxDefaultCommandSemantics is the parity half
// of supervising a pane that was created with no command of its own.
func TestShellPaneLaunchReproducesTmuxDefaultCommandSemantics(t *testing.T) {
	t.Parallel()

	spec := superviseSpec{PaneUID: "pane-1", Generation: "gen-1"}
	for _, test := range []struct {
		name      string
		shell     string
		command   string
		wantChild []string
		wantArgv0 string
	}{
		{
			name: "empty default-command is a login shell", shell: "/usr/bin/zsh",
			wantChild: []string{"/usr/bin/zsh"}, wantArgv0: "-zsh",
		},
		{
			name: "a default-command runs under the default shell", shell: "/bin/bash", command: "exec fish",
			wantChild: []string{"/bin/bash", "-c", "exec fish"}, wantArgv0: "bash",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := &stubDefaultsRunner{shell: test.shell, command: test.command}
			launch := newLaunchMaterializer(runner, &strings.Builder{}).
				supervisedLaunch(context.Background(), spec, nil)
			want := superviseArgv(testSupervisorBinary, spec, test.wantArgv0, test.wantChild)
			if strings.Join(launch, "\x00") != strings.Join(want, "\x00") {
				t.Fatalf("launch = %v, want %v", launch, want)
			}
		})
	}
}

// TestUnsupervisableLaunchStillStartsThePane is the unknown-safe fallback at
// materialization time.
func TestUnsupervisableLaunchStillStartsThePane(t *testing.T) {
	t.Parallel()

	spec := superviseSpec{PaneUID: "pane-1", Generation: "gen-1"}
	t.Run("an unresolvable binary", func(t *testing.T) {
		t.Parallel()
		var warn strings.Builder
		runtime := newLaunchMaterializer(&stubDefaultsRunner{shell: "/bin/sh"}, &warn)
		runtime.executable = func() (string, error) { return "", errors.New("no executable") }
		launch := runtime.supervisedLaunch(context.Background(), spec, []string{"nvim"})
		if strings.Join(launch, " ") != "nvim" {
			t.Fatalf("launch = %v, want the caller's command untouched", launch)
		}
		if !strings.Contains(warn.String(), "without termination evidence") {
			t.Fatalf("warnings = %q", warn.String())
		}
	})
	t.Run("an unreadable tmux default", func(t *testing.T) {
		t.Parallel()
		var warn strings.Builder
		runner := &stubDefaultsRunner{err: errors.New("no server")}
		launch := newLaunchMaterializer(runner, &warn).supervisedLaunch(context.Background(), spec, nil)
		if len(launch) != 0 {
			t.Fatalf("launch = %v, want tmux's own default", launch)
		}
		if !strings.Contains(warn.String(), "default-shell") {
			t.Fatalf("warnings = %q", warn.String())
		}
	})
	t.Run("a Pane with no generation", func(t *testing.T) {
		t.Parallel()
		launch := newLaunchMaterializer(&stubDefaultsRunner{shell: "/bin/sh"}, &strings.Builder{}).
			supervisedLaunch(context.Background(), superviseSpec{PaneUID: "pane-1"}, []string{"nvim"})
		if strings.Join(launch, " ") != "nvim" {
			t.Fatalf("launch = %v, want the caller's command untouched", launch)
		}
	})
}

// TestEveryManagedLaunchCarriesItsOwnGeneration walks the create and resume
// routes and proves each materialized Pane is launched with the generation the
// registry stored for it.
func TestEveryManagedLaunchCarriesItsOwnGeneration(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "create pane", args: []string{"pane", "--project", "alpha", "--window", "main"}},
		{name: "create window", args: []string{"window", "--project", "alpha"}},
		{name: "create agent", args: []string{"agent", "--project", "alpha", "--window", "main", "--provider", "codex"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			create, _ := newTestAgentCreateCommand(t, store, tmux)
			if _, _, err := runRoute(t, create, test.args...); err != nil {
				t.Fatalf("%s error = %v", test.name, err)
			}
			launched := map[string]string{}
			for _, call := range tmux.calls {
				argv := tmuxCommandArgv(call)
				if len(argv) == 0 || (argv[0] != "split-window" && argv[0] != "new-window") {
					continue
				}
				launch := trailingCommand(argv)
				paneUID, generation := supervisedIdentity(launch)
				if paneUID == "" {
					t.Fatalf("%s launched an unsupervised pane: %v", test.name, launch)
				}
				launched[paneUID] = generation
			}
			if len(launched) == 0 {
				t.Fatalf("%s created no pane", test.name)
			}
			for paneUID, generation := range launched {
				pane, ok := store.registry.Pane(paneUID)
				if !ok {
					t.Fatalf("%s launched unknown Pane %q", test.name, paneUID)
				}
				if pane.Status.Activation.Generation != generation {
					t.Fatalf("%s Pane %s stored generation %q but launched %q",
						test.name, paneUID, pane.Status.Activation.Generation, generation)
				}
				if pane.Status.Activation.RuntimeID == "" {
					t.Fatalf("%s Pane %s recorded no exact runtime handle", test.name, paneUID)
				}
			}
			// Every generation is distinct: a fan-out never hands two panes the
			// same receipt identity.
			seen := map[string]string{}
			for paneUID, generation := range launched {
				if other, ok := seen[generation]; ok {
					t.Fatalf("Panes %s and %s share generation %q", other, paneUID, generation)
				}
				seen[generation] = paneUID
			}
		})
	}
}

// TestResumeIssuesAFreshGeneration proves a relaunched Agent stops answering
// to the generation the process it replaced still holds.
func TestResumeIssuesAFreshGeneration(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	setFixtureSessionRef(t, store, "agt-beta-codex", resumeFixtureRef(resourceFixtureClock))
	tmux := newFakeTmux()
	agent, _, _, _ := newTestAgentResumeCommand(t, store, tmux)
	if _, _, err := runRoute(t, agent, "resume", "codex", "--project", "beta"); err != nil {
		t.Fatalf("agent resume error = %v", err)
	}
	launched := splitWindowCalls(tmux)
	if len(launched) != 1 {
		t.Fatalf("resume launched %d panes, want one", len(launched))
	}
	paneUID, generation := supervisedIdentity(trailingCommand(launched[0]))
	if paneUID == "" || generation == "" {
		t.Fatalf("resume launched an unsupervised pane: %v", launched[0])
	}
	pane, ok := store.registry.Pane(paneUID)
	if !ok || pane.Status.Activation.Generation != generation {
		t.Fatalf("resumed Pane %s stores %q but launched %q", paneUID, pane.Status.Activation.Generation, generation)
	}
	if pane.Status.Activation.AgentUID != "agt-beta-codex" {
		t.Fatalf("resumed activation is bound to %q, want the resumed Agent", pane.Status.Activation.AgentUID)
	}

	// A receipt from the generation this resume replaced changes nothing.
	outcome, err := store.mutator().RecordTermination(&store.registry, coremetadata.TerminationEvidence{
		Source:         coremetadata.TerminationSourceSupervisor,
		Classification: coremetadata.TerminationAbnormal,
		PaneUID:        paneUID,
		AgentUID:       "agt-beta-codex",
		Generation:     generation + "-previous",
		Signal:         "HUP",
	})
	if err != nil || outcome.Applied || !outcome.Stale {
		t.Fatalf("previous-generation receipt = %#v, err=%v", outcome, err)
	}
	if after, _ := store.registry.Pane(paneUID); after.Status.LastTermination != nil {
		t.Fatalf("a receipt from the replaced process reached the resumed Pane: %#v", after.Status.LastTermination)
	}
}

// supervisedIdentity reads the Pane uid and generation out of a launched argv.
func supervisedIdentity(argv []string) (string, string) {
	var paneUID, generation string
	for i := 0; i+1 < len(argv); i++ {
		switch argv[i] {
		case "--":
			return paneUID, generation
		case "--pane-uid":
			paneUID = argv[i+1]
		case "--generation":
			generation = argv[i+1]
		}
	}
	return paneUID, generation
}
