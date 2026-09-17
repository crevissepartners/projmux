package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// paneCWDRunner answers the one live read the `pane` split source performs and
// records who was asked. Everything else falls through to the in-memory tmux
// server, so a recorded argv comparison still sees the whole create.
type paneCWDRunner struct {
	tmuxCommandRunner
	cwds  map[string]string
	errs  map[string]error
	reads []string
}

func (r *paneCWDRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	argv := tmuxCommandArgv(args)
	if name == "tmux" && len(argv) > 0 && argv[0] == "display-message" && flagValue(argv, "-F") == "#{pane_current_path}" {
		target := flagValue(argv, "-t")
		r.reads = append(r.reads, target)
		if err, ok := r.errs[target]; ok {
			return nil, err
		}
		return []byte(r.cwds[target] + "\n"), nil
	}
	return r.tmuxCommandRunner.Run(ctx, name, args...)
}

// splitCWDFixture is the alpha runtime with a Project root that really exists,
// so the root rule runs its production filesystem canonicalization.
type splitCWDFixture struct {
	t          *testing.T
	store      *fakeResourceStore
	tmux       *fakeTmux
	create     *createCommand
	launcher   *fakeAgentLauncher
	runner     *paneCWDRunner
	root       string
	configHome string
	anchorID   string
	windowName string
}

// retargetAlphaRoot moves the fixture Project onto a real directory. The tmux
// session's own project-path option moves with it so runtime ownership still
// agrees with the Registry.
func retargetAlphaRoot(t *testing.T, store *fakeResourceStore, tmux *fakeTmux) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp Project root: %v", err)
	}
	project, ok := store.registry.Project("prj-alpha")
	if !ok {
		t.Fatal("fixture has no prj-alpha Project")
	}
	previous := project.Spec.Root
	project.Spec.Root = root
	store.dirs[root] = true
	for i := range store.registry.Panes {
		if store.registry.Panes[i].Spec.CWD == previous {
			store.registry.Panes[i].Spec.CWD = root
		}
	}
	for i := range store.registry.Agents {
		if store.registry.Agents[i].Spec.Workspace.CWD == previous {
			store.registry.Agents[i].Spec.Workspace.CWD = root
		}
	}
	for _, session := range tmux.sessions {
		if session.opts[tmuxopts.ProjectPathSession] == previous {
			session.opts[tmuxopts.ProjectPathSession] = root
		}
	}
	return root
}

func newSplitCWDFixture(t *testing.T) *splitCWDFixture {
	t.Helper()
	store, tmux := aliveAlphaRuntime(t)
	create, launcher := newTestAgentCreateCommand(t, store, tmux)
	fx := &splitCWDFixture{t: t, store: store, tmux: tmux, create: create, launcher: launcher}
	fx.root = retargetAlphaRoot(t, store, tmux)
	fx.wire(create)
	window, ok := store.registry.Window("win-alpha-main")
	if !ok {
		t.Fatal("fixture has no win-alpha-main Window")
	}
	fx.windowName = window.Metadata.Name
	if window.Spec.AnchorPaneRef != "pan-alpha-zsh" {
		t.Fatalf("fixture anchor = %q, want pan-alpha-zsh", window.Spec.AnchorPaneRef)
	}
	fx.anchorID = livePaneWithUID(t, tmux, window.Spec.AnchorPaneRef)
	return fx
}

// wire replaces the command's runtime runner with the recording decorator and
// points its config seams at an isolated config home, so no test reads the
// developer's own global config.
func (f *splitCWDFixture) wire(create *createCommand) {
	f.runner = &paneCWDRunner{tmuxCommandRunner: f.tmux, cwds: map[string]string{}, errs: map[string]error{}}
	create.runtime.runner = f.runner
	f.configHome = f.t.TempDir()
	create.homeDir = func() (string, error) { return f.configHome, nil }
	create.lookupEnv = func(key string) string {
		if key == "XDG_CONFIG_HOME" {
			return f.configHome
		}
		return ""
	}
	f.create = create
}

func (f *splitCWDFixture) subdir(elem ...string) string {
	f.t.Helper()
	dir := filepath.Join(append([]string{f.root}, elem...)...)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatalf("create %q: %v", dir, err)
	}
	return dir
}

func (f *splitCWDFixture) writeProjectConfig(body string) {
	f.t.Helper()
	dir := filepath.Join(f.root, ".projmux")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatalf("create project config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		f.t.Fatalf("write project config: %v", err)
	}
}

func (f *splitCWDFixture) writeGlobalConfig(body string) {
	f.t.Helper()
	path, err := hooks.GlobalConfigPath(f.create.lookupEnv, f.create.homeDir)
	if err != nil {
		f.t.Fatalf("resolve global config path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatalf("create global config dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		f.t.Fatalf("write global config: %v", err)
	}
}

// splitCWDRun is everything acceptance 1 compares between two creates.
type splitCWDRun struct {
	stdout   string
	stderr   string
	err      string
	calls    string
	registry string
	reads    []string
}

func (f *splitCWDFixture) capture(args ...string) splitCWDRun {
	f.t.Helper()
	stdout, stderr, err := runRoute(f.t, f.create, args...)
	return splitCWDRun{
		stdout:   f.render(stdout),
		stderr:   f.render(stderr),
		err:      f.render(errorText(err)),
		calls:    f.render(renderTmuxCalls(f.tmux.calls)),
		registry: f.render(renderSplitCWDRegistry(f.store)),
		reads:    f.runner.reads,
	}
}

// render rewrites this fixture's run-unique directories to stable placeholders,
// so two fixtures that differ only by their temp paths compare byte for byte.
func (f *splitCWDFixture) render(text string) string {
	if f.root != "" {
		text = strings.ReplaceAll(text, f.root, "<root>")
	}
	if f.configHome != "" {
		text = strings.ReplaceAll(text, f.configHome, "<config>")
	}
	return text
}

func renderTmuxCalls(calls [][]string) string {
	var b strings.Builder
	for _, call := range calls {
		fmt.Fprintf(&b, "%s\n", strings.Join(tmuxCommandArgv(call), " "))
	}
	return b.String()
}

// renderSplitCWDRegistry projects exactly the launch-directory surface: the
// stored Pane working directory and the Agent workspace the provider launches
// in, which the shared fixture snapshot does not carry.
func renderSplitCWDRegistry(store *fakeResourceStore) string {
	var b strings.Builder
	for _, pane := range store.registry.Panes {
		fmt.Fprintf(&b, "pane %s %s cwd=%s\n", pane.Metadata.UID, pane.Metadata.Name, pane.Spec.CWD)
	}
	for _, agent := range store.registry.Agents {
		fmt.Fprintf(&b, "agent %s %s cwd=%s roots=%v\n", agent.Metadata.UID, agent.Metadata.Name,
			agent.Spec.Workspace.CWD, agent.Spec.Workspace.AdditionalWritableRoots)
	}
	return b.String()
}

// firstDivergence names the first line two recordings disagree on, so a failure
// points at the change instead of printing two long transcripts.
func firstDivergence(a, b string) string {
	left := strings.Split(a, "\n")
	right := strings.Split(b, "\n")
	for i := 0; i < len(left) || i < len(right); i++ {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		if l != r {
			return fmt.Sprintf("line %d:\n  first:  %q\n  second: %q", i+1, l, r)
		}
	}
	return "no divergence"
}

// splitArgvCWD returns the -c value of the one split this create issued.
func splitArgvCWD(t *testing.T, calls [][]string) string {
	t.Helper()
	found := ""
	splits := 0
	for _, call := range calls {
		argv := tmuxCommandArgv(call)
		if len(argv) == 0 || argv[0] != "split-window" {
			continue
		}
		splits++
		found = flagValue(argv, "-c")
	}
	if splits != 1 {
		t.Fatalf("recorded %d split-window calls, want exactly one:\n%s", splits, renderTmuxCalls(calls))
	}
	return found
}

// TestCLISplitCWDSourceFollowsOnlyTheFlag pins the CLI half of the split: a
// CLI result is determined by its arguments. cliSplitCWDSource takes no config
// seam at all, so a human changing a Settings value cannot move where a script
// starts; with no flag the source is unconditionally `project`.
func TestCLISplitCWDSourceFollowsOnlyTheFlag(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		flag string
		want splitCWDSource
	}{
		{flag: "", want: splitCWDFromProject},
		{flag: "   ", want: splitCWDFromProject},
		{flag: "project", want: splitCWDFromProject},
		{flag: "pane", want: splitCWDFromPane},
		{flag: "  pane  ", want: splitCWDFromPane},
		{flag: "sideways", want: splitCWDFromProject},
	} {
		if got := cliSplitCWDSource(test.flag); got != test.want {
			t.Fatalf("cliSplitCWDSource(%q) = %q, want %q", test.flag, got, test.want)
		}
	}
}

// TestUISplitCWDSourceResolvesFlagThenProjectThenGlobalThenDefault is the UI
// half: the closed precedence, the owner-root project tier, the skipped tier an
// unknown value produces, and the tier label the Settings row shows.
func TestUISplitCWDSourceResolvesFlagThenProjectThenGlobalThenDefault(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	home := t.TempDir()
	lookupEnv := func(key string) string {
		if key == "XDG_CONFIG_HOME" {
			return home
		}
		return ""
	}
	homeDir := func() (string, error) { return home, nil }
	writeProject := func(body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(root, ".projmux"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".projmux", "config.toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeGlobal := func(body string) {
		t.Helper()
		path, err := hooks.GlobalConfigPath(lookupEnv, homeDir)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	resolve := func(flag, projectRoot string) splitCWDResolution {
		t.Helper()
		return resolveUISplitCWDSource(flag, projectRoot, homeDir, lookupEnv)
	}

	if got := resolve("", root); got.Source != splitCWDFromProject || got.Origin != splitCWDOriginDefault {
		t.Fatalf("no config resolution = %+v, want project/default", got)
	}
	writeGlobal("[ai]\nsplit_cwd_from = \"pane\"\n")
	if got := resolve("", root); got.Source != splitCWDFromPane || got.Origin != splitCWDOriginGlobal {
		t.Fatalf("global resolution = %+v, want pane/global", got)
	}
	writeProject("[ai]\nsplit_cwd_from = \"project\"\n")
	if got := resolve("", root); got.Source != splitCWDFromProject || got.Origin != splitCWDOriginProject {
		t.Fatalf("project tier did not win over global: %+v", got)
	}
	if got := resolve("pane", root); got.Source != splitCWDFromPane || got.Origin != splitCWDOriginFlag {
		t.Fatalf("flag did not win over project config: %+v", got)
	}
	// An unknown value skips its own tier rather than deciding or failing.
	writeProject("[ai]\nsplit_cwd_from = \"sideways\"\n")
	if got := resolve("", root); got.Source != splitCWDFromPane || got.Origin != splitCWDOriginGlobal {
		t.Fatalf("unknown project value = %+v, want the global pane tier", got)
	}
	writeGlobal("[ai]\nsplit_cwd_from = \"sideways\"\n")
	if got := resolve("", root); got.Source != splitCWDFromProject || got.Origin != splitCWDOriginDefault {
		t.Fatalf("unknown global value = %+v, want the project default", got)
	}
	// The project tier follows the owner Project root, never a Pane directory.
	writeProject("[ai]\nsplit_cwd_from = \"pane\"\n")
	elsewhere := t.TempDir()
	if err := os.MkdirAll(filepath.Join(elsewhere, ".projmux"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(elsewhere, ".projmux", "config.toml"),
		[]byte("[ai]\nsplit_cwd_from = \"project\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := resolve("", root); got.Source != splitCWDFromPane || got.Origin != splitCWDOriginProject {
		t.Fatalf("owner root tier = %+v, want pane/project", got)
	}
	if got := resolve("", elsewhere); got.Source != splitCWDFromProject || got.Origin != splitCWDOriginProject {
		t.Fatalf("another root's config = %+v, want project/project", got)
	}
}

// TestSplitPaneCWDStaysInsideTheOwnerProjectRoot is acceptance criteria 2, 3
// and 8 at the rule itself: what is kept, what falls back, and that both sides
// are canonicalized before the tree comparison.
func TestSplitPaneCWDStaysInsideTheOwnerProjectRoot(t *testing.T) {
	t.Parallel()

	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "project")
	sub := filepath.Join(root, "services", "api")
	outside := filepath.Join(base, "elsewhere")
	for _, dir := range []string{root, sub, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rootLink := filepath.Join(base, "project-link")
	if err := os.Symlink(root, rootLink); err != nil {
		t.Fatal(err)
	}
	subLink := filepath.Join(base, "api-link")
	if err := os.Symlink(sub, subLink); err != nil {
		t.Fatal(err)
	}
	outsideLink := filepath.Join(base, "elsewhere-link")
	if err := os.Symlink(outside, outsideLink); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name       string
		root       string
		live       string
		readErr    error
		wantDir    string
		wantNotice string
	}{
		{name: "inside the root", root: root, live: sub, wantDir: sub},
		{name: "the root itself keeps the stored spelling", root: root, live: root, wantDir: root},
		{name: "outside the root", root: root, live: outside, wantDir: root, wantNotice: "is outside it"},
		{name: "missing directory", root: root, live: filepath.Join(root, "gone"), wantDir: root, wantNotice: "could not be read"},
		{name: "read failure", root: root, readErr: errors.New("tmux: no server"), wantDir: root, wantNotice: "could not be read"},
		{name: "empty read", root: root, live: "  ", wantDir: root, wantNotice: "could not be read"},
		{name: "symlinked root spelling", root: rootLink, live: sub, wantDir: sub},
		{name: "symlinked live spelling", root: root, live: subLink, wantDir: sub},
		{name: "symlink pointing outside", root: root, live: outsideLink, wantDir: root, wantNotice: "is outside it"},
		{name: "symlinked root and live agree", root: rootLink, live: rootLink, wantDir: rootLink},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir, notice := resolveSplitPaneCWD(test.root, test.live, test.readErr)
			if dir != test.wantDir {
				t.Fatalf("dir = %q, want %q (notice %q)", dir, test.wantDir, notice)
			}
			switch {
			case test.wantNotice == "" && notice != "":
				t.Fatalf("a usable Pane directory produced a notice: %q", notice)
			case test.wantNotice != "":
				if !strings.Contains(notice, test.wantNotice) {
					t.Fatalf("notice = %q, want substring %q", notice, test.wantNotice)
				}
				if !strings.Contains(notice, test.root) {
					t.Fatalf("notice = %q, want the root %q named", notice, test.root)
				}
				if strings.Contains(notice, "\n") {
					t.Fatalf("notice is not one line: %q", notice)
				}
			}
		})
	}
}

// TestSplitCWDDefaultAndProjectSourceAreByteIdentical pins the CLI baseline.
//
// The default, an explicit `--cwd-from project`, a configured `project` and --
// since the CLI stopped reading the config tiers -- a configured `pane` must
// all produce the same recorded tmux argv, the same Registry launch surface,
// the same stdout and stderr, and no live directory read at all. The last case
// is the re-aimed one: config no longer decides anything on a CLI route.
func TestSplitCWDDefaultAndProjectSourceAreByteIdentical(t *testing.T) {
	t.Parallel()

	for _, route := range []struct {
		name string
		argv []string
	}{
		{name: "create pane", argv: []string{"pane", "-p", "alpha", "-o", "pane-id"}},
		{name: "create agent claude", argv: []string{"agent", "--provider", "claude", "-p", "alpha", "-o", "pane-id"}},
		{name: "create codex shortcut", argv: []string{"codex", "-p", "alpha", "-o", "pane-id"}},
	} {
		t.Run(route.name, func(t *testing.T) {
			t.Parallel()

			baseline := newSplitCWDFixture(t)
			baselineRun := baseline.capture(route.argv...)
			if baselineRun.err != "" {
				t.Fatalf("baseline create failed: %s (stderr %q)", baselineRun.err, baselineRun.stderr)
			}

			flagged := newSplitCWDFixture(t)
			flaggedRun := flagged.capture(append(append([]string{}, route.argv...), "--cwd-from", "project")...)

			configured := newSplitCWDFixture(t)
			configured.writeProjectConfig("[ai]\nsplit_cwd_from = \"project\"\n")
			configuredRun := configured.capture(route.argv...)

			// Both config tiers ask for `pane`; a CLI create must not notice.
			ignored := newSplitCWDFixture(t)
			ignored.writeProjectConfig("[ai]\nsplit_cwd_from = \"pane\"\n")
			ignored.writeGlobalConfig("[ai]\nsplit_cwd_from = \"pane\"\n")
			ignored.runner.cwds[ignored.anchorID] = ignored.subdir("services", "api")
			ignoredRun := ignored.capture(route.argv...)

			for _, other := range []struct {
				name string
				run  splitCWDRun
			}{
				{name: "--cwd-from project", run: flaggedRun},
				{name: "configured project", run: configuredRun},
				{name: "configured pane", run: ignoredRun},
			} {
				if other.run.calls != baselineRun.calls {
					t.Fatalf("%s changed the recorded tmux argv: %s",
						other.name, firstDivergence(baselineRun.calls, other.run.calls))
				}
				if other.run.registry != baselineRun.registry {
					t.Fatalf("%s changed the Registry launch surface:\nbaseline:\n%s\n%s:\n%s",
						other.name, baselineRun.registry, other.name, other.run.registry)
				}
				if other.run.stdout != baselineRun.stdout || other.run.stderr != baselineRun.stderr {
					t.Fatalf("%s changed the streams: stdout %q/%q stderr %q/%q",
						other.name, other.run.stdout, baselineRun.stdout, other.run.stderr, baselineRun.stderr)
				}
				if len(other.run.reads) != 0 {
					t.Fatalf("%s read a live Pane directory: %v", other.name, other.run.reads)
				}
			}
			if len(baselineRun.reads) != 0 {
				t.Fatalf("the default source read a live Pane directory: %v", baselineRun.reads)
			}
		})
	}
}

// TestSplitCWDDefaultIntentAndPaneMenuAreByteIdentical is the UI half of
// acceptance criterion 1.
func TestSplitCWDDefaultIntentAndPaneMenuAreByteIdentical(t *testing.T) {
	t.Parallel()

	runIntent := func(t *testing.T, configure func(*splitCWDFixture)) (string, string, string, []string, string) {
		t.Helper()
		fx := newSplitCWDIntentFixture(t)
		if configure != nil {
			configure(fx)
		}
		var stdout, stderr bytes.Buffer
		_, err := fx.create.createFromIntent(agentPaneIntent{
			producer: canonicalProducerDirectShell, placement: "right", anchorPaneID: fx.anchorID,
		}, &stdout, &stderr)
		if err != nil {
			t.Fatalf("intent create failed: %v (stderr %q)", err, stderr.String())
		}
		return fx.render(stdout.String()), fx.render(stderr.String()), fx.render(renderTmuxCalls(fx.tmux.calls)),
			fx.runner.reads, fx.render(renderSplitCWDRegistry(fx.store))
	}

	stdout, stderr, calls, reads, registry := runIntent(t, nil)
	if len(reads) != 0 {
		t.Fatalf("the default UI split read a live Pane directory: %v", reads)
	}
	configuredStdout, configuredStderr, configuredCalls, configuredReads, configuredRegistry := runIntent(t, func(fx *splitCWDFixture) {
		fx.writeProjectConfig("[ai]\nsplit_cwd_from = \"project\"\n")
	})
	if configuredCalls != calls || configuredRegistry != registry ||
		configuredStdout != stdout || configuredStderr != stderr || len(configuredReads) != 0 {
		t.Fatalf("a configured project source changed the UI split: calls %s; registry %s; stdout %q vs %q; stderr %q vs %q; reads %v",
			firstDivergence(calls, configuredCalls), firstDivergence(registry, configuredRegistry),
			stdout, configuredStdout, stderr, configuredStderr, configuredReads)
	}
}

// newSplitCWDIntentFixture is the canonical UI-origin fixture on a real Project
// root, with the same recording runner and isolated config home.
func newSplitCWDIntentFixture(t *testing.T) *splitCWDFixture {
	t.Helper()
	base := canonicalFixture(t, false)
	fx := &splitCWDFixture{t: t, store: base.store, tmux: base.tmux, create: base.create}
	fx.root = retargetAlphaRoot(t, base.store, base.tmux)
	fx.wire(base.create)
	fx.anchorID = base.originID
	window, ok := base.store.registry.Window(base.windowUID)
	if !ok {
		t.Fatalf("fixture has no Window %s", base.windowUID)
	}
	fx.windowName = window.Metadata.Name
	return fx
}

// TestCreateSplitStartsInTheActivePaneDirectory is acceptance criterion 2: the
// shell Pane, the Agent workspace and the split argv all start in the active
// Pane's directory when the `pane` source selects it.
func TestCreateSplitStartsInTheActivePaneDirectory(t *testing.T) {
	t.Parallel()

	t.Run("create pane", func(t *testing.T) {
		t.Parallel()
		fx := newSplitCWDFixture(t)
		sub := fx.subdir("services", "api")
		fx.runner.cwds[fx.anchorID] = sub
		run := fx.capture("pane", "-p", "alpha", "--cwd-from", "pane", "-o", "pane-id")
		if run.err != "" {
			t.Fatalf("create failed: %s", run.err)
		}
		if got := splitArgvCWD(t, fx.tmux.calls); got != sub {
			t.Fatalf("split -c = %q, want %q", got, sub)
		}
		if len(fx.runner.reads) != 1 || fx.runner.reads[0] != fx.anchorID {
			t.Fatalf("live directory reads = %v, want exactly one of %s", fx.runner.reads, fx.anchorID)
		}
		if run.stderr != "" {
			t.Fatalf("a usable Pane directory wrote to stderr: %q", run.stderr)
		}
		if !strings.Contains(run.registry, fx.render("cwd="+sub)) {
			t.Fatalf("no Pane stored the active directory:\n%s", run.registry)
		}
	})

	for _, provider := range []string{"claude", "codex"} {
		t.Run("create "+provider, func(t *testing.T) {
			t.Parallel()
			fx := newSplitCWDFixture(t)
			sub := fx.subdir("services", provider)
			fx.runner.cwds[fx.anchorID] = sub
			run := fx.capture(provider, "-p", "alpha", "--cwd-from", "pane", "-o", "pane-id")
			if run.err != "" {
				t.Fatalf("create failed: %s", run.err)
			}
			if got := splitArgvCWD(t, fx.tmux.calls); got != sub {
				t.Fatalf("split -c = %q, want %q", got, sub)
			}
			plans := fx.launcher.plans
			if len(plans) == 0 {
				t.Fatal("the provider launch was never planned")
			}
			last := plans[len(plans)-1]
			if last.workspace.CWD != sub {
				t.Fatalf("Agent workspace CWD = %q, want %q", last.workspace.CWD, sub)
			}
			if !strings.Contains(run.registry, fx.render("cwd="+sub)) {
				t.Fatalf("the Agent workspace was not stored:\n%s", run.registry)
			}
		})
	}

	t.Run("UI intent", func(t *testing.T) {
		t.Parallel()
		fx := newSplitCWDIntentFixture(t)
		sub := fx.subdir("services", "ui")
		fx.runner.cwds[fx.anchorID] = sub
		fx.writeProjectConfig("[ai]\nsplit_cwd_from = \"pane\"\n")
		var stdout, stderr bytes.Buffer
		if _, err := fx.create.createFromIntent(agentPaneIntent{
			producer: canonicalProducerDirectShell, placement: "right", anchorPaneID: fx.anchorID,
		}, &stdout, &stderr); err != nil {
			t.Fatalf("intent create failed: %v (stderr %q)", err, stderr.String())
		}
		if got := splitArgvCWD(t, fx.tmux.calls); got != sub {
			t.Fatalf("split -c = %q, want %q", got, sub)
		}
		if stderr.Len() != 0 {
			t.Fatalf("a usable Pane directory produced a notice: %q", stderr.String())
		}
		if !strings.Contains(renderSplitCWDRegistry(fx.store), "cwd="+sub) {
			t.Fatalf("the UI split did not store the active directory:\n%s", renderSplitCWDRegistry(fx.store))
		}
	})
}

// TestCreateSplitFallsBackToTheProjectRootWithOneNotice is acceptance criterion
// 3: a Pane directory that cannot be used starts the split in the root, says so
// once, and never refuses.
func TestCreateSplitFallsBackToTheProjectRootWithOneNotice(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		setUp  func(*splitCWDFixture)
		reason string
	}{
		{
			name:   "outside the Project root",
			setUp:  func(fx *splitCWDFixture) { fx.runner.cwds[fx.anchorID] = fx.t.TempDir() },
			reason: "is outside it",
		},
		{
			name: "another registered Project root",
			setUp: func(fx *splitCWDFixture) {
				other := fx.t.TempDir()
				project, ok := fx.store.registry.Project("prj-beta")
				if !ok {
					fx.t.Fatal("fixture has no prj-beta")
				}
				project.Spec.Root = other
				fx.store.dirs[other] = true
				fx.runner.cwds[fx.anchorID] = other
			},
			reason: "is outside it",
		},
		{
			name:   "a directory that no longer exists",
			setUp:  func(fx *splitCWDFixture) { fx.runner.cwds[fx.anchorID] = filepath.Join(fx.root, "gone") },
			reason: "could not be read",
		},
		{
			name:   "an unreadable Pane",
			setUp:  func(fx *splitCWDFixture) { fx.runner.errs[fx.anchorID] = errors.New("tmux: no such pane") },
			reason: "could not be read",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fx := newSplitCWDFixture(t)
			test.setUp(fx)
			baseline := newSplitCWDFixture(t).capture("pane", "-p", "alpha", "-o", "pane-id")
			run := fx.capture("pane", "-p", "alpha", "--cwd-from", "pane", "-o", "pane-id")
			if run.err != "" {
				t.Fatalf("a fallback refused the create: %s", run.err)
			}
			if got := splitArgvCWD(t, fx.tmux.calls); got != fx.root {
				t.Fatalf("split -c = %q, want the Project root %q", got, fx.root)
			}
			if run.stdout != baseline.stdout {
				t.Fatalf("stdout changed: %q, want %q", run.stdout, baseline.stdout)
			}
			lines := strings.Split(strings.TrimSuffix(run.stderr, "\n"), "\n")
			if len(lines) != 1 || !strings.Contains(lines[0], test.reason) || !strings.Contains(lines[0], "<root>") {
				t.Fatalf("stderr = %q, want exactly one line naming %q and the root", run.stderr, test.reason)
			}
			if !strings.HasPrefix(lines[0], canonicalCreatePane+": window/"+fx.windowName+": ") {
				t.Fatalf("stderr line = %q, want the route and Window named", lines[0])
			}
		})
	}
}

// TestSplitCWDFallbackReachesTheClientOnce is the UI half of acceptance
// criterion 3: the Pane menu shows the notice on its one success message, and
// the split-UI funnel displays it once on the originating client.
func TestSplitCWDFallbackReachesTheClientOnce(t *testing.T) {
	t.Parallel()

	fx := newSplitCWDIntentFixture(t)
	fx.runner.cwds[fx.anchorID] = t.TempDir()
	fx.writeProjectConfig("[ai]\nsplit_cwd_from = \"pane\"\n")
	menu := &tmuxCommand{runner: fx.tmux, paneMenuCreate: fx.create.createFromIntent}
	var stdout, stderr bytes.Buffer
	if err := menu.Run([]string{"pane-menu", "--client", "/dev/pts/7", "split-right", fx.anchorID}, &stdout, &stderr); err != nil {
		t.Fatalf("pane menu split failed: %v", err)
	}
	if got := splitArgvCWD(t, fx.tmux.calls); got != fx.root {
		t.Fatalf("Pane menu split -c = %q, want the Project root %q", got, fx.root)
	}
	var messages []string
	for _, message := range fx.tmux.clientMessages {
		if message.client == "/dev/pts/7" {
			messages = append(messages, message.text)
		}
	}
	if len(messages) != 1 {
		t.Fatalf("client messages = %v, want exactly one", messages)
	}
	if !strings.HasPrefix(messages[0], paneMenuCreatedMessage) || !strings.Contains(messages[0], "is outside it") {
		t.Fatalf("client message = %q, want the create result carrying the split start notice", messages[0])
	}

	// The keybinding and launcher funnel owns its own client, so the same
	// notice becomes exactly one display-message there.
	var displayed [][]string
	ai := &aiCommand{
		panes: splitNoticeCreator{notice: "split started in Project root \"/srv/alpha\": active Pane directory \"/tmp\" is outside it"},
		lookupEnv: func(key string) string {
			if key == canonicalCreateTargetClientEnv {
				return "/dev/pts/9"
			}
			return ""
		},
		runCommand: func(_ context.Context, name string, args ...string) error {
			displayed = append(displayed, append([]string{name}, args...))
			return nil
		},
	}
	if err := ai.createPaneFromIntent(agentPaneIntent{producer: canonicalProducerDirectShell, placement: "right"}); err != nil {
		t.Fatalf("split UI funnel failed: %v", err)
	}
	if len(displayed) != 1 {
		t.Fatalf("funnel tmux calls = %v, want exactly one display-message", displayed)
	}
	argv := displayed[0]
	if argv[0] != "tmux" || argv[1] != "display-message" || argv[2] != "-c" || argv[3] != "/dev/pts/9" ||
		!strings.Contains(argv[len(argv)-1], "is outside it") {
		t.Fatalf("funnel display = %v, want one client-scoped split start notice", argv)
	}
}

// splitNoticeCreator is a canonical create that succeeds and reports one split
// start notice, which is the only stderr a committed split writes.
type splitNoticeCreator struct{ notice string }

func (c splitNoticeCreator) createFromIntent(_ agentPaneIntent, _, stderr io.Writer) (createdPaneRuntime, error) {
	_, _ = fmt.Fprintln(stderr, c.notice)
	return createdPaneRuntime{}, nil
}

// TestSplitCWDFlagRefusalsCreateNothing is acceptance criterion 4.
func TestSplitCWDFlagRefusalsCreateNothing(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		argv []string
		want string
	}{
		{
			name: "with an explicit --cwd",
			argv: []string{"claude", "-p", "alpha", "--cwd", ".", "--cwd-from", "pane"},
			want: "cannot be combined with --cwd",
		},
		{
			name: "an unknown source",
			argv: []string{"pane", "-p", "alpha", "--cwd-from", "sideways"},
			want: "must be one of: project, pane",
		},
		{
			name: "an empty source",
			argv: []string{"pane", "-p", "alpha", "--cwd-from", ""},
			want: "must be one of: project, pane",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fx := newSplitCWDFixture(t)
			before := fx.store.snapshot()
			stdout, stderr, err := runRoute(t, fx.create, test.argv...)
			if err == nil || !IsUsageError(err) {
				t.Fatalf("error = %v, want a usage error so exit 2 keeps meaning invalid input", err)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %q, want substring %q", err.Error(), test.want)
			}
			if stdout != "" {
				t.Fatalf("a refused create wrote to stdout: %q", stdout)
			}
			if len(fx.tmux.calls) != 0 {
				t.Fatalf("a refused create issued tmux commands:\n%s", renderTmuxCalls(fx.tmux.calls))
			}
			if fx.store.snapshot() != before {
				t.Fatal("a refused create changed the Registry")
			}
			_ = stderr
		})
	}

	// `--cwd` alone keeps naming the working directory outright and reads no
	// split start config at all.
	fx := newSplitCWDFixture(t)
	sub := fx.subdir("explicit")
	elsewhere := fx.subdir("services", "api")
	fx.runner.cwds[fx.anchorID] = elsewhere
	fx.writeProjectConfig("[ai]\nsplit_cwd_from = \"pane\"\n")
	run := fx.capture("claude", "-p", "alpha", "--cwd", sub, "-o", "pane-id")
	if run.err != "" {
		t.Fatalf("create with --cwd failed: %s", run.err)
	}
	if len(fx.runner.reads) != 0 {
		t.Fatalf("--cwd read a live Pane directory: %v", fx.runner.reads)
	}
	if got := splitArgvCWD(t, fx.tmux.calls); got != sub {
		t.Fatalf("split -c = %q, want the explicit --cwd %q", got, sub)
	}
}

// TestSplitCWDActivePaneIsTheAnchorNotTheFocus is acceptance criterion 6: the
// directory comes from the Pane the create is anchored on, even when another
// Pane is focused and sits somewhere else.
func TestSplitCWDActivePaneIsTheAnchorNotTheFocus(t *testing.T) {
	t.Parallel()

	t.Run("CLI anchors on the resolved anchor Pane", func(t *testing.T) {
		t.Parallel()
		fx := newSplitCWDFixture(t)
		anchorDir := fx.subdir("anchor")
		focusDir := fx.subdir("focus")
		fx.runner.cwds[fx.anchorID] = anchorDir
		// Every other live Pane reports a different directory, so a read of the
		// wrong Pane cannot pass.
		for _, session := range fx.tmux.sessions {
			for _, window := range session.windows {
				for _, pane := range window.panes {
					if pane.id != fx.anchorID {
						fx.runner.cwds[pane.id] = focusDir
					}
				}
			}
		}
		run := fx.capture("pane", "-p", "alpha", "--cwd-from", "pane", "-o", "pane-id")
		if run.err != "" {
			t.Fatalf("create failed: %s", run.err)
		}
		if got := splitArgvCWD(t, fx.tmux.calls); got != anchorDir {
			t.Fatalf("split -c = %q, want the anchor Pane directory %q", got, anchorDir)
		}
		if len(fx.runner.reads) != 1 || fx.runner.reads[0] != fx.anchorID {
			t.Fatalf("reads = %v, want exactly the anchor Pane %s", fx.runner.reads, fx.anchorID)
		}
	})

	t.Run("the Pane menu anchors on the clicked Pane", func(t *testing.T) {
		t.Parallel()
		fx := newSplitCWDIntentFixture(t)
		clicked := fx.subdir("clicked")
		fx.runner.cwds[fx.anchorID] = clicked
		fx.writeProjectConfig("[ai]\nsplit_cwd_from = \"pane\"\n")
		menu := &tmuxCommand{runner: fx.tmux, paneMenuCreate: fx.create.createFromIntent}
		var stdout, stderr bytes.Buffer
		if err := menu.Run([]string{"pane-menu", "--client", "/dev/pts/7", "split-down", fx.anchorID}, &stdout, &stderr); err != nil {
			t.Fatalf("pane menu split failed: %v", err)
		}
		if got := splitArgvCWD(t, fx.tmux.calls); got != clicked {
			t.Fatalf("split -c = %q, want the clicked Pane directory %q", got, clicked)
		}
		if len(fx.runner.reads) != 1 || fx.runner.reads[0] != fx.anchorID {
			t.Fatalf("reads = %v, want exactly the clicked Pane %s", fx.runner.reads, fx.anchorID)
		}
	})
}

// TestSplitCWDLeavesExcludedRoutesUnchanged is acceptance criterion 7: the
// routes this Phase excludes read no live directory and produce the same result
// with the `pane` source selected as without it.
func TestSplitCWDLeavesExcludedRoutesUnchanged(t *testing.T) {
	t.Parallel()

	t.Run("a resumed conversation", func(t *testing.T) {
		t.Parallel()
		resume := func(configure func(*splitCWDFixture)) (string, []string, string) {
			fx := newSplitCWDIntentFixture(t)
			fx.runner.cwds[fx.anchorID] = fx.subdir("services", "api")
			if configure != nil {
				configure(fx)
			}
			var stdout, stderr bytes.Buffer
			_, err := fx.create.createFromIntent(agentPaneIntent{
				producer: canonicalProducerResumePicker, provider: aiModeClaude, placement: "right",
				conversationID: "conv-7", anchorPaneID: fx.anchorID,
			}, &stdout, &stderr)
			if err != nil {
				t.Fatalf("resume intent failed: %v (stderr %q)", err, stderr.String())
			}
			return fx.render(renderTmuxCalls(fx.tmux.calls)), fx.runner.reads, fx.render(stderr.String())
		}
		calls, reads, stderr := resume(nil)
		configuredCalls, configuredReads, configuredStderr := resume(func(fx *splitCWDFixture) {
			fx.writeProjectConfig("[ai]\nsplit_cwd_from = \"pane\"\n")
		})
		if len(reads) != 0 || len(configuredReads) != 0 {
			t.Fatalf("a resume read a live Pane directory: %v / %v", reads, configuredReads)
		}
		if calls != configuredCalls || stderr != configuredStderr {
			t.Fatalf("the pane source changed a resume: %s (stderr %q vs %q)",
				firstDivergence(calls, configuredCalls), stderr, configuredStderr)
		}
	})

	t.Run("a ControlSession-owned Window", func(t *testing.T) {
		t.Parallel()
		control := func(configure func(*splitCWDFixture)) (string, []string) {
			base := canonicalFixture(t, true)
			fx := &splitCWDFixture{t: t, store: base.store, tmux: base.tmux}
			window, ok := base.store.registry.Window(base.windowUID)
			if !ok {
				t.Fatalf("fixture has no Window %s", base.windowUID)
			}
			anchor, ok := base.store.registry.Pane(window.Spec.AnchorPaneRef)
			if !ok {
				t.Fatalf("fixture has no anchor Pane %s", window.Spec.AnchorPaneRef)
			}
			// A ControlSession launch directory is the origin Pane's own stored
			// cwd; normalize it so two fixtures compare byte for byte.
			fx.root = anchor.Spec.CWD
			fx.wire(base.create)
			fx.anchorID = base.originID
			if configure != nil {
				configure(fx)
			}
			// A directory the pane source would have taken if it ran here.
			fx.runner.cwds[fx.anchorID] = fx.t.TempDir()
			var stdout, stderr bytes.Buffer
			if _, err := fx.create.createFromIntent(agentPaneIntent{
				producer: canonicalProducerDirectShell, placement: "right", anchorPaneID: fx.anchorID,
			}, &stdout, &stderr); err != nil {
				t.Fatalf("ControlSession intent failed: %v (stderr %q)", err, stderr.String())
			}
			return fx.render(renderTmuxCalls(fx.tmux.calls)), fx.runner.reads
		}
		calls, reads := control(nil)
		configuredCalls, configuredReads := control(func(fx *splitCWDFixture) {
			fx.writeGlobalConfig("[ai]\nsplit_cwd_from = \"pane\"\n")
		})
		if len(reads) != 0 || len(configuredReads) != 0 {
			t.Fatalf("a ControlSession split read a live Pane directory: %v / %v", reads, configuredReads)
		}
		if calls != configuredCalls {
			t.Fatalf("the pane source changed a ControlSession split: %s", firstDivergence(calls, configuredCalls))
		}
	})

	t.Run("create window keeps its owner and session", func(t *testing.T) {
		t.Parallel()
		fx := newSplitCWDFixture(t)
		fx.writeGlobalConfig("[ai]\nsplit_cwd_from = \"pane\"\n")
		fx.runner.cwds[fx.anchorID] = fx.subdir("services", "api")
		run := fx.capture("window", "-p", "alpha", "-o", "pane-id")
		if run.err != "" {
			t.Fatalf("create window failed: %s", run.err)
		}
		if len(fx.runner.reads) != 0 {
			t.Fatalf("create window read a live Pane directory: %v", fx.runner.reads)
		}
		project, ok := fx.store.registry.Project("prj-alpha")
		if !ok {
			t.Fatal("the Project disappeared")
		}
		if project.Status.Session == nil || project.Status.Session.Name != "alpha" {
			t.Fatalf("the Project session changed: %+v", project.Status.Session)
		}
		if project.Spec.Root != fx.root {
			t.Fatalf("the Project root changed: %q", project.Spec.Root)
		}
		for _, call := range fx.tmux.calls {
			argv := tmuxCommandArgv(call)
			if len(argv) > 0 && argv[0] == "new-window" && flagValue(argv, "-c") != fx.root {
				t.Fatalf("a new Window started outside the Project root: %v", argv)
			}
		}
	})
}

// TestSplitCWDNeverWritesTheScopeIdentity pins the separation the architecture
// decision requires: the resolved launch directory is not the scope's cwd, so
// it cannot rename a session or move an owner.
func TestSplitCWDNeverWritesTheScopeIdentity(t *testing.T) {
	t.Parallel()

	fx := newSplitCWDIntentFixture(t)
	sub := fx.subdir("services", "api")
	fx.runner.cwds[fx.anchorID] = sub
	fx.writeProjectConfig("[ai]\nsplit_cwd_from = \"pane\"\n")

	var sessionNames []string
	fx.create.sessionNameFor = func(root string) string {
		sessionNames = append(sessionNames, root)
		return filepath.Base(root)
	}
	var stdout, stderr bytes.Buffer
	if _, err := fx.create.createFromIntent(agentPaneIntent{
		producer: canonicalProducerDirectShell, placement: "right", anchorPaneID: fx.anchorID,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("intent create failed: %v (stderr %q)", err, stderr.String())
	}
	for _, name := range sessionNames {
		if name != fx.root {
			t.Fatalf("session naming saw %q, want the Project root %q", name, fx.root)
		}
	}
	project, ok := fx.store.registry.Project("prj-alpha")
	if !ok {
		t.Fatal("the Project disappeared")
	}
	if project.Spec.Root != fx.root {
		t.Fatalf("the Project root changed to %q", project.Spec.Root)
	}
	window, ok := fx.store.registry.Window("win-alpha-main")
	if !ok || window.Metadata.OwnerUID() != "prj-alpha" {
		t.Fatal("the Window owner changed")
	}
	if got := splitArgvCWD(t, fx.tmux.calls); got != sub {
		t.Fatalf("split -c = %q, want %q", got, sub)
	}
}

// countSplitCWDConfigReads swaps the UI tier's two config readers for counting
// wrappers and restores them afterwards. Counting at the seam is what makes the
// CLI claim falsifiable: a test that only looked at the outcome would also pass
// if the file had been opened and merely failed to parse.
//
// It mutates a package variable, so its callers must not run in parallel.
func countSplitCWDConfigReads(t *testing.T) *int {
	t.Helper()

	original := splitCWDConfigSeam
	count := 0
	splitCWDConfigSeam = splitCWDConfigReaders{
		project: func(path string) (hooks.ProjectConfig, error) {
			count++
			return original.project(path)
		},
		global: func(path string) (hooks.ProjectConfig, error) {
			count++
			return original.global(path)
		},
	}
	t.Cleanup(func() { splitCWDConfigSeam = original })
	return &count
}

// TestCLISplitCreateOpensNoSplitStartConfig measures the CLI/UI boundary at the
// config seam itself. With `[ai] split_cwd_from = "pane"` set in *both* tiers, a
// CLI create with no `--cwd-from` starts in the Project root and opens neither
// config file; the same configuration still moves a UI intent, which proves the
// counter is wired and the tiers still work where they are supposed to.
//
// No t.Parallel: this test swaps the package-level config seam.
func TestCLISplitCreateOpensNoSplitStartConfig(t *testing.T) {
	reads := countSplitCWDConfigReads(t)

	fx := newSplitCWDFixture(t)
	sub := fx.subdir("services", "api")
	fx.runner.cwds[fx.anchorID] = sub
	fx.writeProjectConfig("[ai]\nsplit_cwd_from = \"pane\"\n")
	fx.writeGlobalConfig("[ai]\nsplit_cwd_from = \"pane\"\n")

	run := fx.capture("pane", "-p", "alpha", "-o", "pane-id")
	if run.err != "" {
		t.Fatalf("create failed: %s (stderr %q)", run.err, run.stderr)
	}
	if *reads != 0 {
		t.Fatalf("a CLI create opened %d split start config file(s); it must resolve from --cwd-from alone", *reads)
	}
	if got := splitArgvCWD(t, fx.tmux.calls); got != fx.root {
		t.Fatalf("split -c = %q, want the Project root %q", got, fx.root)
	}
	if len(fx.runner.reads) != 0 {
		t.Fatalf("a CLI create read a live Pane directory: %v", fx.runner.reads)
	}
	if run.stderr != "" {
		t.Fatalf("an ignored config wrote to stderr: %q", run.stderr)
	}

	// The control: the identical project config still decides a UI split.
	*reads = 0
	ui := newSplitCWDIntentFixture(t)
	uiSub := ui.subdir("services", "ui")
	ui.runner.cwds[ui.anchorID] = uiSub
	ui.writeProjectConfig("[ai]\nsplit_cwd_from = \"pane\"\n")
	var stdout, stderr bytes.Buffer
	if _, err := ui.create.createFromIntent(agentPaneIntent{
		producer: canonicalProducerDirectShell, placement: "right", anchorPaneID: ui.anchorID,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("UI intent create failed: %v (stderr %q)", err, stderr.String())
	}
	if *reads == 0 {
		t.Fatal("the UI intent opened no split start config; the CLI count above would prove nothing")
	}
	if got := splitArgvCWD(t, ui.tmux.calls); got != uiSub {
		t.Fatalf("UI split -c = %q, want the active Pane directory %q", got, uiSub)
	}
}

var _ canonicalPaneCreator = splitNoticeCreator{}

var _ coremetadata.Kind = coremetadata.KindProject
