package app

import (
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// TestDeleteResolvesOneExactTargetAndNeverTheAppSocket is acceptance criterion
// 5 at the routing layer.
//
// The removed behavior is the point of the table: every row that used to reach
// `-L projmux` now either names the server the invocation asked for or refuses.
func TestDeleteResolvesOneExactTargetAndNeverTheAppSocket(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		flags    deleteSocketFlags
		env      string
		want     explicitTmuxTarget
		wantFail string
	}{
		{
			name:  "an explicit socket name wins over the inherited client",
			flags: deleteSocketFlags{socket: "isolated"},
			env:   "/tmp/tmux-1000/projmux,1,0",
			want:  explicitTmuxTarget{flag: "-L", value: "isolated"},
		},
		{
			name:  "an explicit socket path wins over the inherited client",
			flags: deleteSocketFlags{socketPath: "/tmp/isolated/socket"},
			env:   "/tmp/tmux-1000/projmux,1,0",
			want:  explicitTmuxTarget{flag: "-S", value: "/tmp/isolated/socket"},
		},
		{
			name: "the inherited client routes when no flag is given",
			env:  "/tmp/isolated/socket,4242,0",
			want: explicitTmuxTarget{flag: "-S", value: "/tmp/isolated/socket"},
		},
		{
			name:     "both flags together are a usage refusal",
			flags:    deleteSocketFlags{socket: "isolated", socketPath: "/tmp/isolated/socket"},
			wantFail: "only one of --socket and --socket-path",
		},
		{
			name:     "a relative socket path is refused rather than resolved",
			flags:    deleteSocketFlags{socketPath: "relative/socket"},
			wantFail: "--socket-path must be absolute",
		},
		{
			name:     "outside tmux with no flag the route refuses",
			wantFail: "requires --socket <name> or --socket-path <absolute>",
		},
		{
			name:     "a malformed inherited value is not guessed at",
			env:      "not-an-absolute-path,1,0",
			wantFail: "requires --socket <name> or --socket-path <absolute>",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			target, err := resolveDeleteTarget("delete pane", test.flags,
				func(name string) string {
					if name == "TMUX" {
						return test.env
					}
					return ""
				})
			if test.wantFail != "" {
				if err == nil || !IsUsageError(err) || !strings.Contains(err.Error(), test.wantFail) {
					t.Fatalf("error = %v, want a usage refusal mentioning %q", err, test.wantFail)
				}
				if target != (explicitTmuxTarget{}) {
					t.Fatalf("a refused resolution still produced %#v", target)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolve error = %v", err)
			}
			if target != test.want {
				t.Fatalf("target = %#v, want %#v", target, test.want)
			}
			if target.flag == "-L" && target.value == defaultAppSocket {
				t.Fatal("the resolution reached the app socket")
			}
		})
	}
}

func TestExplicitLiveSocketPathIsAmbientInvariant(t *testing.T) {
	t.Parallel()
	flags := deleteSocketFlags{socketPath: "/tmp/isolated/socket"}
	var results []explicitTmuxTarget
	for _, ambient := range []string{"", "/tmp/tmux-1000/projmux,1,0", "/tmp/foreign/socket,9999,7"} {
		target, err := resolveDeleteTarget("delete window", flags, func(name string) string {
			if name == "TMUX" {
				return ambient
			}
			return ""
		})
		if err != nil {
			t.Fatalf("ambient %q: %v", ambient, err)
		}
		results = append(results, target)
	}
	want := explicitTmuxTarget{flag: "-S", value: "/tmp/isolated/socket"}
	for index, got := range results {
		if got != want {
			t.Fatalf("ambient row %d target = %#v, want unchanged live %#v", index, got, want)
		}
	}
}

// TestDeleteInstallsTheResolvedTargetOnTheLiveHalf proves the route's own
// resolution is what the inventory and the kills route through, for both kinds.
func TestDeleteInstallsTheResolvedTargetOnTheLiveHalf(t *testing.T) {
	t.Parallel()

	t.Run("pane", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		cmd := newTestDeleteCommand(store, false, false, nil)
		runtime := newFixturePaneDeleteRuntime()
		cmd.panes = runtime
		if _, _, err := runRoute(t, cmd, "pane", "log", "--project", "alpha", "--window", "main",
			"--socket", "isolated-run", "--dry-run"); err != nil {
			t.Fatalf("dry-run error = %v", err)
		}
		if want := (explicitTmuxTarget{flag: "-L", value: "isolated-run"}); runtime.boundTarget != want {
			t.Fatalf("pane runtime target = %#v, want %#v", runtime.boundTarget, want)
		}
	})

	t.Run("window", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		cmd := newTestDeleteCommand(store, false, false, nil)
		runtime := newFixtureWindowDeleteRuntime()
		cmd.windows = runtime
		if _, _, err := runRoute(t, cmd, "window", "uid:win-alpha-main",
			"--socket-path", "/tmp/isolated/socket", "--dry-run"); err != nil {
			t.Fatalf("dry-run error = %v", err)
		}
		if want := (explicitTmuxTarget{flag: "-S", value: "/tmp/isolated/socket"}); runtime.boundTarget != want {
			t.Fatalf("window runtime target = %#v, want %#v", runtime.boundTarget, want)
		}
	})

	t.Run("agent inherits the same routing as pane", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		cmd := newTestDeleteCommand(store, false, false, nil)
		runtime := newFixturePaneDeleteRuntime()
		cmd.panes = runtime
		if _, _, err := runRoute(t, cmd, "agent", "codex", "--project", "alpha",
			"--socket", "isolated-run", "--dry-run"); err != nil {
			t.Fatalf("dry-run error = %v", err)
		}
		if want := (explicitTmuxTarget{flag: "-L", value: "isolated-run"}); runtime.boundTarget != want {
			t.Fatalf("agent delete target = %#v, want %#v", runtime.boundTarget, want)
		}
	})
}

// TestDeleteResultNamesTheResolvedSocket keeps the reported target honest. The
// line used to be a constant `-L/projmux`, which was accurate only while the
// route could reach exactly one server; a result that names the wrong socket is
// worse than no result at all, because it is what an operator audits.
func TestDeleteResultNamesTheResolvedSocket(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "an explicit socket name", args: []string{"--socket", "isolated-run"}, want: "socket=-L/isolated-run"},
		{name: "an explicit socket path", args: []string{"--socket-path", "/tmp/isolated/socket"}, want: "socket=-S//tmp/isolated/socket"},
		{name: "the inherited client", want: "socket=" + testDeleteTarget.label()},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			cmd := newTestDeleteCommand(store, false, false, nil)
			runtime := newFixturePaneDeleteRuntime()
			cmd.panes = runtime
			args := append([]string{"pane", "log", "--project", "alpha", "--window", "main", "--dry-run"}, test.args...)
			stdout, _, err := runRoute(t, cmd, args...)
			if err != nil {
				t.Fatalf("dry-run error = %v", err)
			}
			if !strings.Contains(stdout, test.want) {
				t.Fatalf("result missing %q:\n%s", test.want, stdout)
			}
			if defaultLabel := "socket=-L/" + defaultAppSocket; test.want != defaultLabel && strings.Contains(stdout, defaultLabel) {
				t.Fatalf("result reported the default app socket:\n%s", stdout)
			}
		})
	}
}

// TestDeleteRefusesOutsideTmuxWithoutTouchingAnything is the containment half:
// a delete that cannot name a server performs no inventory, no kill, and no
// registry write.
func TestDeleteRefusesOutsideTmuxWithoutTouchingAnything(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{"pane", "window", "agent"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			cmd := newTestDeleteCommand(store, false, false, nil)
			cmd.lookupEnv = func(string) string { return "" }
			panes := newFixturePaneDeleteRuntime()
			windows := newFixtureWindowDeleteRuntime()
			cmd.panes, cmd.windows = panes, windows
			before := store.snapshot()

			args := []string{kind, "--all", "--yes"}
			stdout, _, err := runRoute(t, cmd, args...)
			if err == nil || !IsUsageError(err) || !strings.Contains(err.Error(), "--socket") {
				t.Fatalf("delete %s outside tmux = %v, want a usage refusal naming --socket", kind, err)
			}
			if stdout != "" || store.snapshot() != before || store.transactions != 0 {
				t.Fatalf("delete %s refusal mutated state: stdout=%q tx=%d", kind, stdout, store.transactions)
			}
			if panes.preflights != 0 || windows.preflights != 0 {
				t.Fatalf("delete %s refusal still inventoried a server", kind)
			}
		})
	}
}

// TestDeleteRecordsIntentBeforeTheFirstLiveMutation is acceptance criterion 2.
func TestDeleteRecordsIntentBeforeTheFirstLiveMutation(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	runtime := newFixturePaneDeleteRuntime()
	cmd := newTestDeleteCommand(store, false, false, nil)
	cmd.panes = runtime
	var order []string
	runtime.killHook = func(target paneLiveDeleteTarget) {
		pane, ok := store.registry.Pane(target.PaneUID)
		if !ok {
			t.Fatalf("Pane %s was gone before its exact kill", target.PaneUID)
		}
		if pane.Status.LastTermination == nil {
			t.Fatalf("Pane %s was killed with no durable intent recorded", target.PaneUID)
		}
		order = append(order, "kill "+target.PaneUID)
	}
	commit := cmd.store.update
	cmd.store.update = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
		result, err := commit(fn)
		if err == nil {
			order = append(order, "commit")
		}
		return result, err
	}

	if _, _, err := runRoute(t, cmd, "pane", "log", "--project", "alpha", "--window", "main"); err != nil {
		t.Fatalf("delete error = %v", err)
	}
	if got := strings.Join(order, ","); got != "commit,kill pan-alpha-log,commit" {
		t.Fatalf("ordering = %q, want the intent commit, then the exact kill, then the cascade", got)
	}
}

// TestIntentIsRecordedOnlyForProcessesTheDeleteEnds keeps an offline resource
// removal from writing evidence about a process that was never running.
func TestIntentIsRecordedOnlyForProcessesTheDeleteEnds(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	runtime := newFixtureWindowDeleteRuntime()
	// Every Window is offline, so the delete is a pure resource removal.
	runtime.offlineUIDs = map[string]bool{"win-alpha-main": true, "win-alpha-review": true, "win-beta-main": true}
	cmd := newTestDeleteCommand(store, false, false, nil)
	cmd.windows = runtime

	if _, _, err := runRoute(t, cmd, "window", "uid:win-alpha-review", "--yes"); err != nil {
		t.Fatalf("offline window delete error = %v", err)
	}
	if store.writes != 1 {
		t.Fatalf("an offline delete committed %d writes, want only the cascade", store.writes)
	}
	for _, pane := range store.registry.Panes {
		if pane.Status.LastTermination != nil {
			t.Fatalf("an offline delete wrote evidence about %s: %#v", pane.Metadata.UID, pane.Status.LastTermination)
		}
	}
}
