package app

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// stubAgentLauncher satisfies the provider-launch seam without launching
// anything. The Agent route requires the seam to be configured before it
// resolves a scope, so the argv tables below need one that exists and never
// runs. Every method that would create something fails loudly instead.
type stubAgentLauncher struct{ t *testing.T }

func (stubAgentLauncher) RequireAgentEnabled(string) error { return nil }

func (s stubAgentLauncher) PlanAgentLaunch(string, coremetadata.AgentWorkspace, []string) (string, []string, error) {
	s.t.Fatal("the argv tables must never reach the provider launch")
	return "", nil, nil
}

func (s stubAgentLauncher) BindManagedAgentPane(string, string, string, string) {
	s.t.Fatal("the argv tables must never bind a managed pane")
}

func (s stubAgentLauncher) AwaitAgentActivation(context.Context, tmuxCommandRunner, string, time.Duration, time.Duration) (bool, string, error) {
	s.t.Fatal("the argv tables must never await an activation")
	return false, "", nil
}

// newTestCreateCommand builds a create command with nothing wired but the store
// and the active-target seam.
//
// It is deliberately runtime-less: every assertion in this file is about what
// `create` decides from argv alone, so a route that reached a transaction or a
// tmux call would fail with a wiring error rather than quietly passing.
func newTestCreateCommand(t *testing.T, active *recordedActiveTarget) (*createCommand, *fakeResourceStore) {
	t.Helper()
	store := newFakeResourceStore(t)
	return &createCommand{store: store.store(), activeTarget: active.lookup, agents: stubAgentLauncher{t: t}}, store
}

// createRouteSpellings is the closed set of resource-backed create spellings
// this Phase unified, paired with the flag groups each one registers.
var createRouteSpellings = []struct {
	spelling string
	args     []string
	shape    resourceCreateShape
}{
	{spelling: canonicalCreateWindow, args: []string{"window"}, shape: resourceCreateShape{}},
	{spelling: canonicalCreatePane, args: []string{"pane"}, shape: resourceCreateShape{split: true}},
	{spelling: canonicalCreateAgent, args: []string{"agent", "--provider", "codex"}, shape: resourceCreateShape{split: true, provider: true}},
	{spelling: "create codex", args: []string{"codex"}, shape: resourceCreateShape{split: true, provider: true}},
	{spelling: "create claude", args: []string{"claude"}, shape: resourceCreateShape{split: true, provider: true}},
	{spelling: "create antigravity", args: []string{"antigravity"}, shape: resourceCreateShape{split: true, provider: true}},
}

// TestCreateParsesOneFlagSurfaceWithAndWithoutAnExplicitProject is the
// zero-dual-dispatch table.
//
// `--project` is a scope flag, not a mode selector. Each row parses the same
// argv twice -- once with an explicit `--project alpha` and once without it --
// and requires the two parses to be equal everywhere except the scope
// occurrence itself. A route that kept a second parser for the implicit
// spelling shows up here as a "flag provided but not defined" on the implicit
// parse, which is exactly the defect this Phase closes.
func TestCreateParsesOneFlagSurfaceWithAndWithoutAnExplicitProject(t *testing.T) {
	t.Parallel()

	type argvCase struct {
		name  string
		args  []string
		split bool
		agent bool
	}
	cases := []argvCase{
		{name: "bare"},
		{name: "name and labels", args: []string{"--name", "n", "--label", "k=v", "--label", "a=b"}},
		{name: "output", args: []string{"-o", "pane-id"}},
		{name: "payload", args: []string{"--", "htop", "--project", "opaque", "-w", "opaque"}},
		{name: "window fan-out", args: []string{"--window", "main", "-w", "review"}, split: true},
		{name: "create-window", args: []string{"-w", "hi", "--create-window"}, split: true},
		{name: "anchor pane", args: []string{"--window", "main", "--pane", "zsh"}, split: true},
		{name: "label selector", args: []string{"--selector", "role=shell"}, split: true},
		{name: "placement", args: []string{"--placement", "down"}, split: true},
		{name: "workspace cardinality", args: []string{"--cwd", "/srv/alpha", "--add-dir", "/srv/beta", "--add-dir", "/srv/gone"}, agent: true},
	}

	for _, route := range createRouteSpellings {
		for _, test := range cases {
			if test.split && !route.shape.split {
				continue
			}
			if test.agent && !route.shape.provider {
				continue
			}
			t.Run(route.spelling+"/"+test.name, func(t *testing.T) {
				t.Parallel()
				implicit, implicitErr := parseResourceCreateFlags(route.spelling, test.args, &bytes.Buffer{}, route.shape)
				explicit, explicitErr := parseResourceCreateFlags(route.spelling,
					append([]string{"--project", "alpha"}, test.args...), &bytes.Buffer{}, route.shape)
				if implicitErr != nil || explicitErr != nil {
					t.Fatalf("implicit err = %v, explicit err = %v", implicitErr, explicitErr)
				}
				if len(implicit.projects) != 0 {
					t.Fatalf("the implicit parse carried a Project scope: %v", implicit.projects)
				}
				if !reflect.DeepEqual(explicit.projects, repeatedFlag{"alpha"}) {
					t.Fatalf("the explicit parse lost its Project scope: %v", explicit.projects)
				}
				implicit.projects = explicit.projects
				if !reflect.DeepEqual(implicit, explicit) {
					t.Fatalf("one argv parsed two ways\nimplicit=%#v\nexplicit=%#v", implicit, explicit)
				}
			})
		}
	}
}

// TestCreateRepeatedProjectScopeIsRefused pins the remaining cardinality: the
// flag became optional, not repeatable.
func TestCreateRepeatedProjectScopeIsRefused(t *testing.T) {
	t.Parallel()

	_, err := parseResourceCreateFlags(canonicalCreatePane,
		[]string{"-p", "alpha", "--project", "beta"}, &bytes.Buffer{}, resourceCreateShape{split: true})
	if err == nil || !IsUsageError(err) || !strings.Contains(err.Error(), "at most one --project") {
		t.Fatalf("error = %v, want an at-most-one usage error", err)
	}
}

// TestCreateHasNoCompatibilitySplitSeam is the structural half of the same
// claim.
//
// The retired product model needed a raw-argv forwarder to reach `ai split`.
// Removing the discriminator without removing that seam would leave a route one
// edit away from dual dispatch, so the seam itself is asserted gone: the only
// raw-argv forwarders left on `create` are the two parity kinds that never
// created a Projmux resource in the first place.
func TestCreateHasNoCompatibilitySplitSeam(t *testing.T) {
	t.Parallel()

	wantForwarders := map[string]bool{"notify": true, "snapshots": true}
	structType := reflect.TypeFor[createCommand]()
	forwarder := reflect.TypeFor[rawArgvCommand]()
	for i := range structType.NumField() {
		field := structType.Field(i)
		if field.Type != forwarder {
			continue
		}
		if !wantForwarders[field.Name] {
			t.Fatalf("create carries a raw-argv seam %q; every resource kind must be resource-backed", field.Name)
		}
		delete(wantForwarders, field.Name)
	}
	if len(wantForwarders) != 0 {
		t.Fatalf("the parity forwarders disappeared: %v", wantForwarders)
	}
}

// TestCreateRefusesInvalidArgvBeforeConsultingTheRuntime is the argv-only
// refusal table.
//
// Every row is a usage error (exit 2) with zero bytes on stdout. Each one is
// also required to be decided **without** observing the tmux runtime: what the
// operator typed is wrong wherever they typed it, and reporting an
// environment-dependent "pass --project" first would hide the real fix.
func TestCreateRefusesInvalidArgvBeforeConsultingTheRuntime(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "a missing provider never falls back to the saved split mode",
			args: []string{"agent"},
			want: "requires --provider",
		},
		{
			name: "an empty provider is not a provider",
			args: []string{"agent", "--provider", ""},
			want: "requires --provider",
		},
		{
			name: "selective is a picker, not a provider",
			args: []string{"agent", "--provider", "selective"},
			want: "interactive picker, not a provider",
		},
		{
			name: "resume is a picker, not a provider",
			args: []string{"agent", "--provider", "resume"},
			want: "interactive picker, not a provider",
		},
		{
			name: "shell is a Pane, not an Agent provider",
			args: []string{"agent", "--provider", "shell"},
			want: "projmux create pane",
		},
		{
			name: "an unknown provider lists the enum",
			args: []string{"agent", "--provider", "gpt"},
			want: "accepted providers: codex, claude, antigravity",
		},
		{
			name: "a shortcut may not respell the provider it already names",
			args: []string{"codex", "--provider", "codex"},
			want: "already names the provider",
		},
		{
			name: "a shortcut may not override its provider either",
			args: []string{"claude", "--provider", "codex"},
			want: "already names the provider",
		},
		{
			name: "the placement enum is closed",
			args: []string{"agent", "--provider", "codex", "--placement", "left"},
			want: "--placement must be one of: right, down",
		},
		{
			name: "the placement positional is not accepted",
			args: []string{"agent", "--provider", "codex", "down"},
			want: "does not accept positional arguments",
		},
		{
			name: "the legacy force flag is not promoted to a canonical flag",
			args: []string{"agent", "--provider", "codex", "--force-agent"},
			want: "flag provided but not defined: -force-agent",
		},
		{
			name: "the legacy agent flag is not a canonical spelling",
			args: []string{"agent", "--agent", "codex"},
			want: "flag provided but not defined: -agent",
		},
		{
			name: "the print-pane-id boolean is not promoted either",
			args: []string{"agent", "--provider", "codex", "--print-pane-id"},
			want: "flag provided but not defined: -print-pane-id",
		},
		{
			name: "an empty payload is refused",
			args: []string{"agent", "--provider", "codex", "--"},
			want: "-- requires a payload",
		},
		{
			name: "the Pane route takes no provider",
			args: []string{"pane", "--provider", "codex"},
			want: "flag provided but not defined: -provider",
		},
		{
			name: "create window takes no split surface",
			args: []string{"window", "--placement", "down"},
			want: "flag provided but not defined: -placement",
		},
		{
			name: "create-window needs an exact name to create",
			args: []string{"pane", "--create-window"},
			want: "requires at least one exact-name --window",
		},
		{
			name: "create-window and a label selector are mutually exclusive",
			args: []string{"pane", "-w", "hi", "--create-window", "--selector", "role=shell"},
			want: "cannot be combined with --selector",
		},
		{
			name: "a provider is not a resource kind",
			args: []string{"gpt"},
			want: "create gpt is not available",
		},
		{
			name: "selective is not a create shortcut",
			args: []string{"selective"},
			want: "create selective is not available",
		},
		{
			name: "a kind is required",
			args: nil,
			want: "create requires a resource kind",
		},
		{
			name: "cwd is not a create projection",
			args: []string{"agent", "--provider", "codex", "-o", "cwd"},
			want: "invalid --output",
		},
		{
			name: "an unknown output token is refused",
			args: []string{"agent", "--provider", "codex", "-o", "bogus"},
			want: "invalid --output",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			// Inside a fully managed runtime, so a row that only fails because
			// no Project could be derived would not pass this table.
			active := insideTmux("pan-alpha-zsh", "win-alpha-main")
			create, store := newTestCreateCommand(t, active)
			stdout, _, err := runRoute(t, create, test.args...)
			if err == nil {
				t.Fatalf("create %v succeeded", test.args)
			}
			if !IsUsageError(err) {
				t.Fatalf("create %v error is not a usage error: %v", test.args, err)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("create %v error = %q, want it to mention %q", test.args, err, test.want)
			}
			if stdout != "" {
				t.Fatalf("create %v wrote %q to stdout, want 0 bytes", test.args, stdout)
			}
			if active.calls != 0 {
				t.Fatalf("create %v observed the tmux runtime %d times before refusing the argv", test.args, active.calls)
			}
			if store.transactions != 0 || store.writes != 0 {
				t.Fatalf("create %v opened %d transactions and wrote %d times", test.args, store.transactions, store.writes)
			}
		})
	}
}
