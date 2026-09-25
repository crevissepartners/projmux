package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
	"github.com/crevissepartners/projmux/internal/core/candidates"
	"github.com/crevissepartners/projmux/internal/core/lifecycle"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// flagValueUsageCounters counts every lookup a refused flag value must not
// reach: session identity, candidate discovery, the open-route validator, the
// home directory, and the ephemeral inventory.
type flagValueUsageCounters struct {
	identity, discover, openRoute, homeDir, inventory int
}

func (c *flagValueUsageCounters) total() int {
	return c.identity + c.discover + c.openRoute + c.homeDir + c.inventory
}

func (c *flagValueUsageCounters) app() *App {
	return &App{
		switcher: &switchCommand{
			identity: switchIdentityResolverFunc(func(string) (string, error) {
				c.identity++
				return "session", nil
			}),
			discover: func(candidates.Inputs) ([]string, error) {
				c.discover++
				return nil, nil
			},
			validateProjectOpenRoute: func(context.Context, string) error {
				c.openRoute++
				return nil
			},
		},
		attach: &attachCommand{
			homeDir: func() (string, error) {
				c.homeDir++
				return "/home/tester", nil
			},
			inventory: attachInventoryResolverFunc(func(context.Context) ([]lifecycle.SessionInventory, error) {
				c.inventory++
				return nil, nil
			}),
		},
		prune: &pruneCommand{
			inventory: pruneInventoryResolverFunc(func(context.Context) ([]lifecycle.SessionInventory, error) {
				c.inventory++
				return nil, nil
			}),
		},
	}
}

// TestFlagValueRefusalsAreUsageErrorsBeforeAnyLookup pins the flag values the
// catalog synopsis does not spell as a closed set (the form V guard in
// public_route_argv_guard_test.go covers those that it does). Each refusal is
// a usage error (exit 2) with its reason text unchanged, prints its route's
// Usage block exactly once, writes nothing to stdout, and happens before any
// lookup or effect.
func TestFlagValueRefusalsAreUsageErrorsBeforeAnyLookup(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		argv   []string
		reason string
		usage  string
	}{
		{
			name:   "switch --anchor",
			argv:   []string{"switch", "--anchor", "bogus"},
			reason: "switch --anchor requires an exact %N Pane handle",
			usage:  "  projmux switch [--ui popup|sidebar] [--anchor <pane>]",
		},
		{
			name:   "switch sidebar-open --anchor",
			argv:   []string{"switch", "sidebar-open", "--path", "/work/alpha", "--anchor", "bogus"},
			reason: "switch sidebar-open --anchor requires an exact %N Pane handle",
			usage:  "  projmux switch sidebar-open --path <path> --anchor <pane>",
		},
		{
			name:   "switch sidebar-open --mode",
			argv:   []string{"switch", "sidebar-open", "--path", "/work/alpha", "--anchor", "%1", "--client", "/dev/pts/9", "--mode", "bogus"},
			reason: `switch sidebar-open: unknown startup mode "bogus"`,
			usage:  "  projmux switch sidebar-open --path <path> --anchor <pane>",
		},
		{
			name:   "runtime attach --keep",
			argv:   []string{"runtime", "attach", "--keep", "-1"},
			reason: "plan auto attach: ephemeral keep count must be non-negative",
			usage:  "  projmux runtime attach [--keep <n>] [--fallback home|ephemeral]",
		},
		{
			name:   "runtime prune --keep",
			argv:   []string{"runtime", "prune", "--keep", "-1"},
			reason: "plan ephemeral prune: ephemeral keep count must be non-negative",
			usage:  "  projmux runtime prune",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			counters := &flagValueUsageCounters{}
			var stdout, stderr bytes.Buffer
			err := counters.app().Run(test.argv, &stdout, &stderr)
			if err == nil {
				t.Fatal("Run() error = nil, want a usage error")
			}
			if !IsUsageError(err) {
				t.Fatalf("Run() error = %v (%T), want a usage error (exit 2)", err, err)
			}
			if err.Error() != test.reason {
				t.Fatalf("reason = %q, want %q unchanged", err.Error(), test.reason)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want 0 bytes", stdout.String())
			}
			if got := strings.Count(stderr.String(), "Usage:"); got != 1 {
				t.Fatalf("stderr has %d Usage blocks, want 1:\n%s", got, stderr.String())
			}
			if !strings.Contains(stderr.String(), test.usage) {
				t.Fatalf("stderr does not carry the route's own synopsis %q:\n%s", test.usage, stderr.String())
			}
			// The entrypoint prints the reason; the handler must not.
			if strings.Contains(stderr.String(), test.reason) {
				t.Fatalf("handler stderr already carries the reason, so it would print twice:\n%s", stderr.String())
			}
			if counters.total() != 0 {
				t.Fatalf("a refused flag value reached a lookup: %+v", *counters)
			}
		})
	}
}

// TestKeepZeroStillReachesTheInventory is the boundary control for the
// negative --keep refusal: 0 is a valid keep count and is not refused early.
func TestKeepZeroStillReachesTheInventory(t *testing.T) {
	t.Parallel()
	for _, argv := range [][]string{
		{"runtime", "prune", "--keep", "0"},
		{"runtime", "attach", "--keep", "0"},
	} {
		counters := &flagValueUsageCounters{}
		var stderr bytes.Buffer
		err := counters.app().Run(argv, &bytes.Buffer{}, &stderr)
		if IsUsageError(err) {
			t.Fatalf("%q: Run() error = %v, a valid keep count must not be a usage error", argv, err)
		}
		if counters.inventory != 1 {
			t.Fatalf("%q: inventory reads = %d, want 1 (err %v)", argv, counters.inventory, err)
		}
	}
}

// gateCountingAgentLauncher counts the Settings gate on top of the recording
// launcher, so a test can prove a refusal never reached it.
type gateCountingAgentLauncher struct {
	*fakeAgentLauncher
	gates int
}

func (l *gateCountingAgentLauncher) RequireAgentEnabled(provider string) error {
	l.gates++
	return l.fakeAgentLauncher.RequireAgentEnabled(provider)
}

// workspaceUsageFixture is the Agent create fixture with a counter on every
// step a stateless workspace refusal must not reach: the Settings gate, the
// store (read or transaction), the workspace resolver, the launch plan, and
// tmux.
type workspaceUsageFixture struct {
	store     *fakeResourceStore
	tmux      *fakeTmux
	create    *createCommand
	launcher  *gateCountingAgentLauncher
	resolvers int
}

func newWorkspaceUsageFixture(t *testing.T) *workspaceUsageFixture {
	t.Helper()
	f := &workspaceUsageFixture{store: newFakeResourceStore(t), tmux: newFakeTmux()}
	create, launcher := newTestAgentCreateCommand(t, f.store, f.tmux)
	f.launcher = &gateCountingAgentLauncher{fakeAgentLauncher: launcher}
	create.agents = f.launcher
	resolver := create.resolveWorkspace
	create.resolveWorkspace = func(registry coremetadata.Registry, owner coremetadata.Project, provider, cwd string, additional []string) (coremetadata.AgentWorkspace, error) {
		f.resolvers++
		return resolver(registry, owner, provider, cwd, additional)
	}
	f.create = create
	return f
}

func (f *workspaceUsageFixture) reached() map[string]int {
	return map[string]int{
		"settings gate": f.launcher.gates,
		"store reads":   f.store.reads,
		"transactions":  f.store.transactions,
		"writes":        f.store.writes,
		"resolver":      f.resolvers,
		"launch plans":  len(f.launcher.plans),
		"tmux calls":    len(f.tmux.calls),
	}
}

// TestCreateAgentWorkspaceFlagRefusalsAreUsageErrorsBeforeTheLock pins the
// --cwd and --add-dir values no state can rescue. Each is a usage error (exit
// 2) with the resolver's reason text unchanged -- `create agent` even on a
// shortcut, because that is what the resolver has always said -- prints the
// Usage block of the route it was spelled on exactly once and nothing else,
// writes nothing to stdout, and is refused before the Settings gate, the store,
// the resolver, the launcher, and tmux.
func TestCreateAgentWorkspaceFlagRefusalsAreUsageErrorsBeforeTheLock(t *testing.T) {
	t.Parallel()
	type refusal struct {
		name   string
		flags  []string
		reason string
	}
	relativeCWD := refusal{"relative --cwd", []string{"--cwd", "rel/path"},
		`create agent: --cwd "rel/path": must be an absolute existing directory`}
	relativeAddDir := refusal{"relative --add-dir", []string{"--add-dir", "rel"},
		`create agent: --add-dir "rel": must be an absolute existing directory`}
	blankAddDir := refusal{"blank --add-dir", []string{"--add-dir", "  "},
		`create agent: --add-dir "  ": must be an absolute existing directory`}
	laterRelativeAddDir := refusal{"relative --add-dir after an absolute one", []string{"--add-dir", "/a", "--add-dir", "./b"},
		`create agent: --add-dir "./b": must be an absolute existing directory`}
	duplicateAddDir := refusal{"--add-dir duplicates --add-dir", []string{"--add-dir", "/a", "--add-dir", "/a/"},
		`create agent: --add-dir "/a/" duplicates the effective workspace or another explicit root`}
	duplicateCWD := refusal{"--add-dir duplicates --cwd", []string{"--cwd", "/a/b/..", "--add-dir", " /a "},
		`create agent: --add-dir " /a " duplicates the effective workspace or another explicit root`}
	unsupported := refusal{"--add-dir on antigravity", []string{"--add-dir", "/tmp"},
		`create agent: provider "antigravity" does not support additional writable roots`}
	// The provider refusal comes first, exactly as in the resolver.
	unsupportedFirst := refusal{"antigravity refuses --add-dir before a relative --cwd", []string{"--cwd", "rel", "--add-dir", "rel"},
		`create agent: provider "antigravity" does not support additional writable roots`}

	routes := []struct {
		name     string
		spelling string
		argv     []string
		refusals []refusal
	}{
		{"create agent codex", "create agent", []string{"create", "agent", "--provider", "codex"},
			[]refusal{relativeCWD, relativeAddDir, blankAddDir, laterRelativeAddDir, duplicateAddDir, duplicateCWD}},
		{"create agent claude", "create agent", []string{"create", "agent", "--provider", "claude"},
			[]refusal{relativeCWD, relativeAddDir, duplicateAddDir, duplicateCWD}},
		{"create agent antigravity", "create agent", []string{"create", "agent", "--provider", "antigravity"},
			[]refusal{relativeCWD, unsupported, unsupportedFirst}},
		{"create codex", "create codex", []string{"create", "codex"},
			[]refusal{relativeCWD, relativeAddDir, blankAddDir, laterRelativeAddDir, duplicateAddDir, duplicateCWD}},
		{"create claude", "create claude", []string{"create", "claude"},
			[]refusal{relativeCWD, relativeAddDir, duplicateAddDir, duplicateCWD}},
		{"create antigravity", "create antigravity", []string{"create", "antigravity"},
			[]refusal{relativeCWD, unsupported, unsupportedFirst}},
	}
	for _, route := range routes {
		for _, test := range route.refusals {
			t.Run(route.name+"/"+test.name, func(t *testing.T) {
				t.Parallel()
				fixture := newWorkspaceUsageFixture(t)
				argv := append(append(append([]string(nil), route.argv...), test.flags...), "--project", "alpha")
				var stdout, stderr bytes.Buffer
				err := (&App{create: fixture.create}).Run(argv, &stdout, &stderr)
				if err == nil {
					t.Fatal("Run() error = nil, want a usage error")
				}
				if !IsUsageError(err) {
					t.Fatalf("Run() error = %v (%T), want a usage error (exit 2)", err, err)
				}
				if err.Error() != test.reason {
					t.Fatalf("reason = %q, want %q unchanged", err.Error(), test.reason)
				}
				if stdout.Len() != 0 {
					t.Fatalf("stdout = %q, want 0 bytes", stdout.String())
				}
				var usage bytes.Buffer
				cli.WriteRouteUsage(&usage, route.spelling)
				if strings.Count(usage.String(), "Usage:") != 1 {
					t.Fatalf("%s renders no catalog Usage block: %q", route.spelling, usage.String())
				}
				// The handler prints its route's Usage block and nothing else;
				// the entrypoint prints the reason.
				if stderr.String() != usage.String() {
					t.Fatalf("handler stderr = %q, want exactly the %s Usage block %q", stderr.String(), route.spelling, usage.String())
				}
				for step, count := range fixture.reached() {
					if count != 0 {
						t.Fatalf("a refused workspace flag reached the %s (%d): %+v", step, count, fixture.reached())
					}
				}
			})
		}
	}
}

// TestCreateAgentWorkspaceStateRefusalsStayInsideTheTransaction is the
// boundary control: a value that only the filesystem or the Registry can judge
// is not refused early. It reaches the resolver inside the transaction and
// keeps its plain (exit 1) error and its text.
func TestCreateAgentWorkspaceStateRefusalsStayInsideTheTransaction(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	owner := filepath.Join(base, "owner")
	sibling := filepath.Join(base, "sibling")
	for _, dir := range []string{owner, sibling} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	missing := filepath.Join(owner, "missing")
	for _, test := range []struct {
		name   string
		argv   []string
		reason string
	}{
		{"nonexistent --cwd", []string{"agent", "--provider", "codex", "--cwd", missing},
			`create agent: --cwd "` + missing + `": stat ` + missing + `: no such file or directory`},
		{"--cwd outside every Project", []string{"claude", "--cwd", sibling},
			`create agent: --cwd "` + sibling + `" is outside every registered Project root`},
		{"--add-dir outside every Project", []string{"codex", "--add-dir", sibling},
			`create agent: --add-dir "` + sibling + `" is outside every registered Project root`},
		{"--add-dir duplicating the default workspace", []string{"claude", "--add-dir", owner},
			`create agent: --add-dir "` + owner + `" duplicates the effective workspace or another explicit root`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newWorkspaceUsageFixture(t)
			for i := range fixture.store.registry.Projects {
				if fixture.store.registry.Projects[i].Metadata.UID == "prj-alpha" {
					fixture.store.registry.Projects[i].Spec.Root = owner
				}
			}
			fixture.store.dirs[owner] = true
			before := fixture.store.snapshot()

			stdout, stderr, err := runRoute(t, fixture.create, append(test.argv, "--project", "alpha")...)
			if err == nil {
				t.Fatal("Run() error = nil, want the resolver's refusal")
			}
			if IsUsageError(err) {
				t.Fatalf("Run() error = %v is a usage error; a state refusal must stay exit 1", err)
			}
			if err.Error() != test.reason {
				t.Fatalf("reason = %q, want %q unchanged", err.Error(), test.reason)
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want 0 bytes", stdout)
			}
			if strings.Contains(stderr, "Usage:") {
				t.Fatalf("a state refusal printed a Usage block:\n%s", stderr)
			}
			if fixture.resolvers != 1 || fixture.store.transactions != 1 {
				t.Fatalf("resolver calls = %d, transactions = %d, want the resolver reached inside one transaction", fixture.resolvers, fixture.store.transactions)
			}
			if fixture.store.writes != 0 || fixture.store.snapshot() != before || len(fixture.launcher.plans) != 0 {
				t.Fatalf("a refused workspace mutated state: writes=%d plans=%d", fixture.store.writes, len(fixture.launcher.plans))
			}
		})
	}
}

// TestResolveAgentWorkspaceStatelessRefusalsStayPlainErrors pins the stored
// value callers -- a restored UI intent, `agent resume`, the rebinder -- to
// their exit code: the resolver's copies of the argv refusals are plain errors
// with unchanged text, never usage errors.
func TestResolveAgentWorkspaceStatelessRefusalsStayPlainErrors(t *testing.T) {
	t.Parallel()
	owner := t.TempDir()
	registry := coremetadata.NewRegistry()
	registry.Projects = []coremetadata.Project{{Metadata: coremetadata.ObjectMeta{UID: "owner"}, Spec: coremetadata.ProjectSpec{Root: owner}}}
	for _, test := range []struct {
		name, provider, cwd string
		additional          []string
		reason              string
	}{
		{"relative cwd", aiModeCodex, "rel/path", nil,
			`create agent: --cwd "rel/path": must be an absolute existing directory`},
		{"relative add-dir", aiModeClaude, "", []string{"rel"},
			`create agent: --add-dir "rel": must be an absolute existing directory`},
		{"duplicate add-dir", aiModeCodex, owner, []string{owner + "/"},
			`create agent: --add-dir "` + owner + `/" duplicates the effective workspace or another explicit root`},
		{"antigravity add-dir", aiModeAntigravity, "", []string{owner},
			`create agent: provider "antigravity" does not support additional writable roots`},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := resolveAgentWorkspace(registry, registry.Projects[0], test.provider, test.cwd, test.additional)
			if err == nil || IsUsageError(err) {
				t.Fatalf("resolveAgentWorkspace() error = %v, want a plain (non-usage) error", err)
			}
			if err.Error() != test.reason {
				t.Fatalf("reason = %q, want %q unchanged", err.Error(), test.reason)
			}
			// `agent resume` and the rebinder call the resolver under their
			// own spelling; only the prefix differs, never the classification.
			_, err = resolveAgentWorkspaceFor("agent resume", registry, registry.Projects[0], test.provider, test.cwd, test.additional)
			resumeReason := "agent resume" + strings.TrimPrefix(test.reason, canonicalCreateAgent)
			if err == nil || IsUsageError(err) || err.Error() != resumeReason {
				t.Fatalf("resolveAgentWorkspaceFor(agent resume) error = %v, want plain error %q", err, resumeReason)
			}
			// The argv half reaches the same text, only as a usage error.
			pre := refuseStatelessAgentWorkspace(test.provider, test.cwd, test.additional)
			if pre == nil || !IsUsageError(pre) || pre.Error() != test.reason {
				t.Fatalf("refuseStatelessAgentWorkspace() = %v, want usage error %q", pre, test.reason)
			}
		})
	}
}
