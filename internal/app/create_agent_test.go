package app

import (
	"errors"
	"flag"
	"strings"
	"sync"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// fakeAgentLauncher stands in for the provider launch half of the AI command.
//
// It records what the canonical route asked for rather than what it did, which
// is what makes two contract properties directly assertable: that the payload
// reaches the launch and never the naming, and that the enabled-agents gate runs
// before anything is allocated.
type fakeAgentLauncher struct {
	mu sync.Mutex
	// disabled names the providers the Settings gate refuses.
	disabled map[string]bool
	// planErr fails the launch construction, the way a missing agent binary does.
	planErr error
	// plans records one entry per launch construction.
	plans []fakeLaunchRequest
	// bound records one entry per managed-pane binding.
	bound []fakeBoundPane
}

type fakeLaunchRequest struct {
	provider   string
	contextDir string
	payload    []string
}

type fakeBoundPane struct {
	paneID   string
	provider string
	title    string
}

func newFakeAgentLauncher() *fakeAgentLauncher {
	return &fakeAgentLauncher{disabled: map[string]bool{}}
}

func (f *fakeAgentLauncher) RequireAgentEnabled(provider string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.disabled[provider] {
		// The legacy handler returns a plain error here, so the canonical route
		// has to as well: the classification, and therefore the exit code, is
		// what the contract fixes, not the wording.
		return errors.New("AI agent " + provider + " is disabled in Settings > AI Settings > Enabled agents")
	}
	return nil
}

func (f *fakeAgentLauncher) PlanAgentLaunch(provider, contextDir string, payload []string) (string, []string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.planErr != nil {
		return "", nil, f.planErr
	}
	f.plans = append(f.plans, fakeLaunchRequest{
		provider:   provider,
		contextDir: contextDir,
		payload:    append([]string(nil), payload...),
	})
	argv := []string{"sh", "-lc", "exec " + provider}
	if len(payload) > 0 {
		argv[2] += " " + strings.Join(payload, " ")
	}
	return provider + ":launch", argv, nil
}

func (f *fakeAgentLauncher) BindManagedAgentPane(paneID, provider, _, title string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bound = append(f.bound, fakeBoundPane{paneID: paneID, provider: provider, title: title})
}

// newTestAgentCreateCommand wires the canonical Agent create onto the in-memory
// registry, the in-memory tmux server, and a recording launcher.
func newTestAgentCreateCommand(t *testing.T, store *fakeResourceStore, tmux *fakeTmux) (*createCommand, *fakeAgentLauncher) {
	t.Helper()
	create, _ := newTestResourceCreateCommand(t, store, tmux)
	launcher := newFakeAgentLauncher()
	create.agents = launcher
	return create, launcher
}

// agentNamed returns the created Agent with the given name inside one Window.
func agentNamed(t *testing.T, store *fakeResourceStore, windowUID, name string) coremetadata.Agent {
	t.Helper()
	for _, agent := range store.registry.Agents {
		if agent.Metadata.OwnerUID() == windowUID && agent.Metadata.Name == name {
			return agent
		}
	}
	t.Fatalf("no agent named %q in window %q; registry:\n%s", name, windowUID, store.snapshot())
	return coremetadata.Agent{}
}

// TestCreateAgentRequiresAnExplicitProviderOnEveryCanonicalSpelling is
// acceptance criterion 1.
//
// The saved split mode is not consulted on this route, so a missing provider is
// a usage error before the store is opened rather than a silent launch of
// whatever Settings happens to hold.
func TestCreateAgentRequiresAnExplicitProviderOnEveryCanonicalSpelling(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "no provider at all",
			args: []string{"agent", "--project", "alpha"},
			want: "requires --provider",
		},
		{
			name: "an empty provider is not a provider",
			args: []string{"agent", "--project", "alpha", "--provider", ""},
			want: "requires --provider",
		},
		{
			name: "selective is a picker adapter, not a provider",
			args: []string{"agent", "--project", "alpha", "--provider", "selective"},
			want: "interactive picker, not a provider",
		},
		{
			name: "shell is a Pane, not an Agent",
			args: []string{"agent", "--project", "alpha", "--provider", "shell"},
			want: "projmux create pane",
		},
		{
			name: "an unknown provider lists the enum",
			args: []string{"agent", "--project", "alpha", "--provider", "gpt"},
			want: "accepted providers: codex, claude, antigravity",
		},
		{
			name: "a shortcut may not respell its own provider",
			args: []string{"codex", "--project", "alpha", "--provider", "codex"},
			want: "already names the provider",
		},
		{
			name: "a shortcut may not override its provider either",
			args: []string{"claude", "--project", "alpha", "--provider", "codex"},
			want: "already names the provider",
		},
		{
			name: "the legacy force flag is not promoted to a canonical flag",
			args: []string{"agent", "--project", "alpha", "--provider", "codex", "--force-agent"},
			want: "flag provided but not defined: -force-agent",
		},
		{
			name: "the placement enum stays closed",
			args: []string{"agent", "--project", "alpha", "--provider", "codex", "--placement", "left"},
			want: "--placement must be one of: right, down",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			before := store.snapshot()
			tmux := newFakeTmux()
			create, launcher := newTestAgentCreateCommand(t, store, tmux)

			stdout, _, err := runRoute(t, create, test.args...)
			if err == nil {
				t.Fatalf("create %v succeeded", test.args)
			}
			if !IsUsageError(err) {
				t.Fatalf("create %v is not a usage error: %v", test.args, err)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("create %v error = %q, want it to mention %q", test.args, err, test.want)
			}
			if stdout != "" {
				t.Fatalf("create %v wrote %q to stdout, want 0 bytes", test.args, stdout)
			}
			if store.snapshot() != before {
				t.Fatalf("create %v mutated the registry", test.args)
			}
			if len(launcher.plans) != 0 || len(tmux.calls) != 0 {
				t.Fatalf("create %v reached the launcher %d times and tmux %d times, want 0/0",
					test.args, len(launcher.plans), len(tmux.calls))
			}
		})
	}
}

// TestTheShortcutAndTheCanonicalSpellingProduceIdenticalNaming is acceptance
// criterion 2.
//
// The two spellings run in separate fixtures against the same Window, so an
// identical result is a property of the shared handler rather than of ordering.
func TestTheShortcutAndTheCanonicalSpellingProduceIdenticalNaming(t *testing.T) {
	t.Parallel()

	render := func(t *testing.T, args ...string) (string, string) {
		t.Helper()
		store := newFakeResourceStore(t)
		create, _ := newTestAgentCreateCommand(t, store, newFakeTmux())
		stdout, _, err := runRoute(t, create, args...)
		if err != nil {
			t.Fatalf("create %v error = %v", args, err)
		}
		// The whole registry is compared, not just the result line: the managed
		// Pane name, the owner refs, and the name reservations all have to match
		// too, and only a snapshot catches a divergence in any of them.
		return stdout, store.snapshot()
	}

	for _, provider := range cli.ProviderCreateShortcuts() {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			canonicalOut, canonicalState := render(t,
				"agent", "--provider", provider, "--project", "alpha", "--window", "review")
			shortcutOut, shortcutState := render(t,
				provider, "--project", "alpha", "--window", "review")

			if canonicalOut != shortcutOut {
				t.Fatalf("create %s stdout = %q, canonical = %q", provider, shortcutOut, canonicalOut)
			}
			if canonicalState != shortcutState {
				t.Fatalf("create %s registry differs from the canonical route:\n%s\nwant:\n%s",
					provider, shortcutState, canonicalState)
			}
			if !strings.Contains(canonicalOut, "agent/"+provider+" created") {
				t.Fatalf("result line = %q, want the provider id as the Agent name", canonicalOut)
			}
		})
	}
}

// TestCreateAgentNeverReusesAnExistingAgent is acceptance criterion 3.
//
// The fixture Window already owns a running Agent of the same provider, which is
// exactly the shape an implicit reuse would latch onto. Two further creates have
// to mint two more uids, suffix two more names, and leave the first Agent's pane
// binding untouched.
func TestCreateAgentNeverReusesAnExistingAgent(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	// The fixture Agent's managed Pane has to be a live tmux pane. Reconciliation
	// releases an Agent whose managed Pane is gone, so an unmirrored fixture would
	// make "the existing Agent kept its pane" unobservable for a reason that has
	// nothing to do with reuse.
	seedLiveAgentPane(t, tmux, "alpha", "win-alpha-main", "pan-alpha-zsh", "pan-alpha-codex")
	create, _ := newTestAgentCreateCommand(t, store, tmux)

	existing := agentNamed(t, store, "win-alpha-main", "codex")
	if existing.Status.PaneRef == "" {
		t.Fatal("the fixture Agent has no managed Pane, so reuse would be unobservable")
	}

	var uids []string
	var names []string
	for i := range 2 {
		stdout, _, err := runRoute(t, create, "agent", "--provider", "codex", "--project", "alpha", "--window", "main", "-o", "uid")
		if err != nil {
			t.Fatalf("create %d error = %v", i, err)
		}
		uids = append(uids, strings.TrimSpace(stdout))
	}
	for _, agent := range store.registry.Agents {
		if agent.Metadata.OwnerUID() == "win-alpha-main" {
			names = append(names, agent.Metadata.Name)
		}
	}

	if uids[0] == uids[1] || uids[0] == existing.Metadata.UID || uids[1] == existing.Metadata.UID {
		t.Fatalf("uids = %v, existing = %q; every create must mint a new Agent", uids, existing.Metadata.UID)
	}
	want := []string{"codex", "codex-1", "codex-2"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("agent names in window/main = %v, want %v", names, want)
	}

	// The pre-existing Agent kept its own managed Pane: a create rebinds nothing.
	after := agentNamed(t, store, "win-alpha-main", "codex")
	if after.Metadata.UID != existing.Metadata.UID || after.Status.PaneRef != existing.Status.PaneRef {
		t.Fatalf("the existing Agent was rebound: %+v, was %+v", after.Status, existing.Status)
	}
	if after.Status.Phase != coremetadata.PhaseRunning {
		t.Fatalf("the existing Agent phase = %q, want it untouched", after.Status.Phase)
	}
	// A successful create ends Running with its managed Pane bound. A Failed
	// Agent is never a create outcome.
	for _, uid := range uids {
		agent, ok := store.registry.Agent(uid)
		if !ok {
			t.Fatalf("created agent %q is not in the registry", uid)
		}
		if agent.Status.Phase != coremetadata.PhaseRunning {
			t.Fatalf("created agent %q phase = %q, want Running", uid, agent.Status.Phase)
		}
		pane, ok := store.registry.Pane(agent.Status.PaneRef)
		if !ok {
			t.Fatalf("created agent %q has no managed Pane", uid)
		}
		if want := agent.Metadata.Name + "-pane"; pane.Metadata.Name != want {
			t.Fatalf("managed pane name = %q, want %q", pane.Metadata.Name, want)
		}
		if pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != coremetadata.KindAgent {
			t.Fatalf("managed pane owner = %+v, want the Agent", pane.Metadata.OwnerRef)
		}
	}
	assertNoClientMovement(t, tmux)
}

// TestCreateAgentWindowEnsureIsOptIn is acceptance criterion 4.
func TestCreateAgentWindowEnsureIsOptIn(t *testing.T) {
	t.Parallel()

	t.Run("a missing Window is a no-match without the flag", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		before := store.snapshot()
		tmux := newFakeTmux()
		create, launcher := newTestAgentCreateCommand(t, store, tmux)

		stdout, _, err := runRoute(t, create, "agent", "--provider", "codex", "--project", "alpha", "--window", "fresh")
		if err == nil {
			t.Fatal("a missing Window silently created itself")
		}
		if !IsUsageError(err) {
			t.Fatalf("a missing Window is not a usage error: %v", err)
		}
		if stdout != "" || store.snapshot() != before || len(launcher.bound) != 0 {
			t.Fatalf("a missing Window mutated something: stdout=%q", stdout)
		}
	})

	t.Run("the flag ensures the Window and still returns an Agent", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		tmux := newFakeTmux()
		create, _ := newTestAgentCreateCommand(t, store, tmux)

		stdout, _, err := runRoute(t, create,
			"agent", "--provider", "claude", "--project", "alpha", "--window", "fresh", "--create-window", "-o", "ref")
		if err != nil {
			t.Fatalf("create agent --create-window error = %v", err)
		}
		// The ensured Window is operation context, not the result kind.
		if got := strings.TrimSpace(stdout); got != "agent/claude" {
			t.Fatalf("result = %q, want agent/claude", got)
		}
		var fresh *coremetadata.Window
		for i := range store.registry.Windows {
			if store.registry.Windows[i].Metadata.Name == "fresh" {
				fresh = &store.registry.Windows[i]
			}
		}
		if fresh == nil {
			t.Fatalf("--create-window did not create the Window:\n%s", store.snapshot())
		}
		if fresh.Spec.PrimaryPaneRef == "" {
			t.Fatal("the ensured Window has no primaryPaneRef to anchor on")
		}
		agent := agentNamed(t, store, fresh.Metadata.UID, "claude")
		if agent.Status.Phase != coremetadata.PhaseRunning {
			t.Fatalf("agent phase = %q, want Running", agent.Status.Phase)
		}
		assertNoClientMovement(t, tmux)
	})

	t.Run("--create-window still needs an exact name", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		create, _ := newTestAgentCreateCommand(t, store, newFakeTmux())
		_, _, err := runRoute(t, create,
			"agent", "--provider", "codex", "--project", "alpha", "--selector", "role=shell", "--create-window")
		if err == nil || !IsUsageError(err) {
			t.Fatalf("--create-window with only a label selector = %v, want a usage error", err)
		}
	})
}

// TestAnExplicitAgentNameIsWindowScoped is acceptance criterion 5 plus its
// intentional complement.
//
// Agent names are unique inside a Window, so the same explicit --name in two
// different Windows is legal; a collision inside one Window is exit 2 with no
// implicit suffix and nothing created.
func TestAnExplicitAgentNameIsWindowScoped(t *testing.T) {
	t.Parallel()

	t.Run("the same name in two Windows is legal", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		tmux := newFakeTmux()
		create, _ := newTestAgentCreateCommand(t, store, tmux)

		stdout, _, err := runRoute(t, create,
			"agent", "--provider", "codex", "--project", "alpha", "--window", "main", "--window", "review",
			"--name", "reviewer", "-o", "name")
		if err != nil {
			t.Fatalf("fan-out with one explicit --name error = %v", err)
		}
		if got := strings.Fields(stdout); len(got) != 2 || got[0] != "reviewer" || got[1] != "reviewer" {
			t.Fatalf("names = %v, want reviewer twice", got)
		}
		if a, b := agentNamed(t, store, "win-alpha-main", "reviewer"), agentNamed(t, store, "win-alpha-review", "reviewer"); a.Metadata.UID == b.Metadata.UID {
			t.Fatal("one Agent was bound to two Windows")
		}
	})

	t.Run("a collision inside one Window is exit 2 with zero mutations", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		before := store.snapshot()
		tmux := newFakeTmux()
		create, launcher := newTestAgentCreateCommand(t, store, tmux)

		// `codex` already names an Agent inside alpha's `main` Window.
		stdout, _, err := runRoute(t, create,
			"agent", "--provider", "codex", "--project", "alpha", "--window", "main", "--name", "codex")
		if err == nil {
			t.Fatal("an explicit name collision succeeded")
		}
		if !IsUsageError(err) {
			t.Fatalf("an explicit name collision is not a usage error: %v", err)
		}
		if stdout != "" {
			t.Fatalf("an explicit name collision wrote %q to stdout", stdout)
		}
		if store.snapshot() != before {
			t.Fatalf("an explicit name collision mutated the registry:\n%s", store.snapshot())
		}
		if store.writes != 0 {
			t.Fatalf("registry writes = %d, want 0", store.writes)
		}
		// No suffix was invented, and nothing reached the runtime.
		if strings.Contains(store.snapshot(), "codex-1") {
			t.Fatal("an explicit name collision fell back to an implicit suffix")
		}
		if tmux.paneCount() != 0 || len(launcher.bound) != 0 {
			t.Fatalf("an explicit name collision created %d panes", tmux.paneCount())
		}
	})
}

// TestTopicAndPromptNeverSeedAnAgentOrPaneName is acceptance criterion 6.
//
// The payload after `--` reaches the provider launch and nothing else. The
// naming has to be identical to a payload-free create of the same provider.
func TestTopicAndPromptNeverSeedAnAgentOrPaneName(t *testing.T) {
	t.Parallel()

	names := func(t *testing.T, args ...string) (string, *fakeAgentLauncher, *fakeResourceStore) {
		t.Helper()
		store := newFakeResourceStore(t)
		create, launcher := newTestAgentCreateCommand(t, store, newFakeTmux())
		if _, _, err := runRoute(t, create, args...); err != nil {
			t.Fatalf("create %v error = %v", args, err)
		}
		var rows []string
		for _, agent := range store.registry.Agents {
			if agent.Metadata.OwnerUID() == "win-alpha-review" {
				rows = append(rows, "agent="+agent.Metadata.Name)
			}
		}
		for _, pane := range store.registry.Panes {
			if agent, ok := store.registry.Agent(pane.Metadata.OwnerUID()); ok && agent.Metadata.OwnerUID() == "win-alpha-review" {
				rows = append(rows, "pane="+pane.Metadata.Name)
			}
		}
		return strings.Join(rows, " "), launcher, store
	}

	bare, _, _ := names(t, "agent", "--provider", "codex", "--project", "alpha", "--window", "review")
	loaded, launcher, store := names(t,
		"agent", "--provider", "codex", "--project", "alpha", "--window", "review",
		"--", "--topic", "release triage", "안녕")

	if bare != loaded {
		t.Fatalf("payload changed the naming: %q, want %q", loaded, bare)
	}
	if bare != "agent=codex pane=codex-pane" {
		t.Fatalf("naming = %q, want the provider id and its managed pane", bare)
	}
	// The payload did reach the launch, so the negative above is about naming
	// rather than about the payload being dropped.
	if len(launcher.plans) != 1 {
		t.Fatalf("launch plans = %d, want 1", len(launcher.plans))
	}
	if got := strings.Join(launcher.plans[0].payload, " "); got != "--topic release triage 안녕" {
		t.Fatalf("payload reaching the launch = %q", got)
	}
	// And it is not stored as a name-derivation source on the managed Pane
	// either, where a later rename could resurrect it.
	for _, pane := range store.registry.Panes {
		if strings.Contains(pane.Spec.Command, "triage") || strings.Contains(pane.Metadata.Name, "triage") {
			t.Fatalf("the payload leaked onto pane %+v", pane.Metadata)
		}
	}
}

// TestCreateAgentRendersEveryAdvertisedProjection is acceptance criterion 7.
//
// Every shared `-o` mode is exercised end to end. The structured modes keep the
// List envelope even for a single result, and `pane-id` is the managed Pane's
// raw transport handle rather than the anchor's.
func TestCreateAgentRendersEveryAdvertisedProjection(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		mode string
		want func(t *testing.T, stdout string, store *fakeResourceStore, tmux *fakeTmux)
	}{
		{
			mode: "",
			want: func(t *testing.T, stdout string, _ *fakeResourceStore, _ *fakeTmux) {
				if stdout != "agent/codex created\n" {
					t.Fatalf("default stdout = %q", stdout)
				}
			},
		},
		{
			mode: "uid",
			want: func(t *testing.T, stdout string, store *fakeResourceStore, _ *fakeTmux) {
				agent := agentNamed(t, store, "win-alpha-review", "codex")
				if strings.TrimSpace(stdout) != agent.Metadata.UID {
					t.Fatalf("uid stdout = %q, want %q", stdout, agent.Metadata.UID)
				}
			},
		},
		{
			mode: "name",
			want: func(t *testing.T, stdout string, _ *fakeResourceStore, _ *fakeTmux) {
				if strings.TrimSpace(stdout) != "codex" {
					t.Fatalf("name stdout = %q", stdout)
				}
			},
		},
		{
			mode: "ref",
			want: func(t *testing.T, stdout string, _ *fakeResourceStore, _ *fakeTmux) {
				if strings.TrimSpace(stdout) != "agent/codex" {
					t.Fatalf("ref stdout = %q", stdout)
				}
			},
		},
		{
			mode: "metadata",
			want: func(t *testing.T, stdout string, _ *fakeResourceStore, _ *fakeTmux) {
				if !strings.Contains(stdout, `"kind": "AgentMetadataList"`) {
					t.Fatalf("metadata stdout = %q, want an AgentMetadataList envelope", stdout)
				}
				if strings.Contains(stdout, `"spec"`) || strings.Contains(stdout, `"paneRef"`) {
					t.Fatalf("metadata stdout leaked non-metadata fields: %q", stdout)
				}
			},
		},
		{
			mode: "json",
			want: func(t *testing.T, stdout string, _ *fakeResourceStore, _ *fakeTmux) {
				if !strings.Contains(stdout, `"kind": "AgentList"`) {
					t.Fatalf("json stdout = %q, want an AgentList envelope", stdout)
				}
				for _, want := range []string{`"provider": "codex"`, `"phase": "Running"`, `"paneRef"`} {
					if !strings.Contains(stdout, want) {
						t.Fatalf("json stdout = %q, want it to carry %s", stdout, want)
					}
				}
			},
		},
		{
			mode: "pane-id",
			want: func(t *testing.T, stdout string, store *fakeResourceStore, tmux *fakeTmux) {
				id := strings.TrimSpace(stdout)
				if !strings.HasPrefix(id, "%") {
					t.Fatalf("pane-id stdout = %q, want a raw %%N handle", stdout)
				}
				agent := agentNamed(t, store, "win-alpha-review", "codex")
				_, _, pane := tmux.pane(id)
				if pane == nil {
					t.Fatalf("pane-id %q is not a live pane", id)
				}
				// It is the managed Pane's binding, not the anchor's.
				if pane.opts["@projmux_pane_uid"] != agent.Status.PaneRef {
					t.Fatalf("pane-id %q mirrors %q, want the managed pane %q",
						id, pane.opts["@projmux_pane_uid"], agent.Status.PaneRef)
				}
			},
		},
		{
			mode: "none",
			want: func(t *testing.T, stdout string, _ *fakeResourceStore, _ *fakeTmux) {
				if stdout != "" {
					t.Fatalf("none stdout = %q, want 0 bytes", stdout)
				}
			},
		},
	} {
		t.Run("o="+test.mode, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			create, _ := newTestAgentCreateCommand(t, store, tmux)

			args := []string{"agent", "--provider", "codex", "--project", "alpha", "--window", "review"}
			if test.mode != "" {
				args = append(args, "-o", test.mode)
			}
			stdout, stderr, err := runRoute(t, create, args...)
			if err != nil {
				t.Fatalf("create agent -o %s error = %v", test.mode, err)
			}
			if stderr != "" {
				t.Fatalf("create agent -o %s wrote %q to stderr on success", test.mode, stderr)
			}
			test.want(t, stdout, store, tmux)
		})
	}
}

// TestCreateAgentFansOutOverEveryWindowAnchorExactlyOnce is the multi-window
// half of acceptance criterion 7.
func TestCreateAgentFansOutOverEveryWindowAnchorExactlyOnce(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, launcher := newTestAgentCreateCommand(t, store, tmux)

	// No --window fans out over every Window of the Project, the same meaning
	// the sibling `create pane --project` route gives the omitted flag.
	stdout, _, err := runRoute(t, create, "agent", "--provider", "codex", "--project", "alpha", "-o", "pane-id")
	if err != nil {
		t.Fatalf("fan-out error = %v", err)
	}
	ids := strings.Fields(stdout)
	if len(ids) != 2 {
		t.Fatalf("pane ids = %v, want one per Window of the Project", ids)
	}
	if ids[0] == ids[1] {
		t.Fatalf("both Windows reported the same pane %q", ids[0])
	}

	// Exactly two splits, each anchored on its own Window's primaryPaneRef.
	var anchors []string
	for _, call := range tmux.calls {
		if len(call) > 0 && call[0] == "split-window" {
			anchors = append(anchors, flagValue(call, "-t"))
		}
	}
	if len(anchors) != 2 {
		t.Fatalf("split-window calls = %v, want exactly 2", anchors)
	}
	if anchors[0] == anchors[1] {
		t.Fatalf("both splits used the same anchor %q", anchors[0])
	}
	if len(launcher.plans) != 1 {
		t.Fatalf("launch constructions = %d, want one shared launch for the fan-out", len(launcher.plans))
	}
	if len(launcher.bound) != 2 {
		t.Fatalf("managed pane bindings = %d, want one per created Agent", len(launcher.bound))
	}
	assertNoClientMovement(t, tmux)
}

// TestAFailureAnywhereInAnAgentFanOutLeavesZeroMutations is the atomicity half
// of acceptance criterion 7.
//
// Each row fails at a different stage -- preflight, metadata allocation, launch
// construction, and the tmux split itself -- and every one has to end with the
// registry byte-identical, stdout empty, and no Agent left behind in any phase.
func TestAFailureAnywhereInAnAgentFanOutLeavesZeroMutations(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		args  []string
		setup func(store *fakeResourceStore, tmux *fakeTmux, launcher *fakeAgentLauncher)
	}{
		{
			name: "a stale anchor in the second Window",
			args: []string{"agent", "--provider", "codex", "--project", "alpha"},
			setup: func(store *fakeResourceStore, _ *fakeTmux, _ *fakeAgentLauncher) {
				for i := range store.registry.Windows {
					if store.registry.Windows[i].Metadata.UID == "win-alpha-review" {
						store.registry.Windows[i].Spec.PrimaryPaneRef = "pan-gone"
					}
				}
			},
		},
		{
			name: "an explicit name that collides in the second Window",
			args: []string{"agent", "--provider", "codex", "--project", "alpha", "--name", "codex"},
		},
		{
			name: "a launch construction failure",
			args: []string{"agent", "--provider", "codex", "--project", "alpha"},
			setup: func(_ *fakeResourceStore, _ *fakeTmux, launcher *fakeAgentLauncher) {
				launcher.planErr = errors.New("codex runner not found")
			},
		},
		{
			name: "a tmux split failure after the first Agent already split",
			args: []string{"agent", "--provider", "codex", "--project", "alpha"},
			setup: func(_ *fakeResourceStore, tmux *fakeTmux, _ *fakeAgentLauncher) {
				tmux.fail = []string{"split-window", "-v"}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			create, launcher := newTestAgentCreateCommand(t, store, tmux)
			if test.setup != nil {
				test.setup(store, tmux, launcher)
			}
			if len(tmux.fail) > 0 {
				// The failing row splits down so the injected trigger matches.
				test.args = append(test.args, "--placement", placementDown)
			}
			before := store.snapshot()
			agentsBefore := len(store.registry.Agents)

			stdout, _, err := runRoute(t, create, test.args...)
			if err == nil {
				t.Fatalf("%s succeeded", test.name)
			}
			if stdout != "" {
				t.Fatalf("%s wrote %q to stdout", test.name, stdout)
			}
			if store.snapshot() != before {
				t.Fatalf("%s mutated the registry:\n%s\nwant:\n%s", test.name, store.snapshot(), before)
			}
			if store.writes != 0 {
				t.Fatalf("%s committed %d registry writes", test.name, store.writes)
			}
			if len(store.registry.Agents) != agentsBefore {
				t.Fatalf("%s left %d Agents behind", test.name, len(store.registry.Agents)-agentsBefore)
			}
			// A launch failure never leaves a Failed Agent: create rolls back
			// whole, and Failed is a lifecycle transition of an Agent that
			// already exists.
			for _, agent := range store.registry.Agents {
				if agent.Status.Phase == coremetadata.PhaseFailed {
					t.Fatalf("%s left a Failed Agent behind: %+v", test.name, agent.Metadata)
				}
			}
			assertNoClientMovement(t, tmux)
		})
	}
}

// TestATmuxFailureRemovesTheAgentPanesTheOperationCreated proves the runtime
// half of the rollback: the ledger takes the created panes back out.
func TestATmuxFailureRemovesTheAgentPanesTheOperationCreated(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, _ := newTestAgentCreateCommand(t, store, tmux)

	// Materialize the runtime with a successful create first, so the failing
	// operation runs against a session it did not create and must not remove.
	if _, _, err := runRoute(t, create, "agent", "--provider", "codex", "--project", "alpha", "--window", "main"); err != nil {
		t.Fatalf("seed create error = %v", err)
	}
	panesBefore := tmux.paneCount()
	windowsBefore := tmux.windowCount()
	sessionsBefore := len(tmux.sessions)

	tmux.fail = []string{"split-window", "-v"}
	if _, _, err := runRoute(t, create,
		"agent", "--provider", "claude", "--project", "alpha", "--placement", placementDown); err == nil {
		t.Fatal("the injected split failure did not fail the create")
	}

	if got := tmux.paneCount(); got != panesBefore {
		t.Fatalf("panes = %d, want %d; rollback left an Agent pane behind:\n%s", got, panesBefore, tmux.state())
	}
	if got := tmux.windowCount(); got != windowsBefore {
		t.Fatalf("windows = %d, want %d", got, windowsBefore)
	}
	if got := len(tmux.sessions); got != sessionsBefore {
		t.Fatalf("sessions = %d, want %d; rollback removed a session it did not create", got, sessionsBefore)
	}
}

// TestConcurrentAgentCreatesConvergeOnOneEnsuredWindow is the racing half of
// acceptance criterion 4, against the real locked registry file.
//
// Six creates ask for the same missing Window at once. The on-disk lock has to
// serialize them onto one Window uid while every racer still gets its own Agent:
// converging must not mean dropping work.
func TestConcurrentAgentCreatesConvergeOnOneEnsuredWindow(t *testing.T) {
	t.Parallel()

	const racers = 6
	fixture := newOnDiskFixture(t, "alpha")
	launcher := newFakeAgentLauncher()

	var wg sync.WaitGroup
	errs := make([]error, racers)
	outs := make([]string, racers)
	start := make(chan struct{})
	for i := range racers {
		wg.Go(func() {
			<-start
			cmd := fixture.command(nil)
			cmd.agents = launcher
			stdout, _, err := runRoute(t, cmd,
				"agent", "--provider", "codex", "--project", "alpha", "--window", "shared", "--create-window", "-o", "uid")
			outs[i], errs[i] = stdout, err
		})
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("racer %d failed: %v", i, err)
		}
	}
	registry := fixture.load(t)
	if err := registry.Validate(); err != nil {
		t.Fatalf("the raced registry does not validate: %v", err)
	}

	var shared []string
	for _, window := range registry.Windows {
		if window.Metadata.Name == "shared" {
			shared = append(shared, window.Metadata.UID)
		}
	}
	if len(shared) != 1 {
		t.Fatalf("Windows named `shared` = %v, want exactly one uid", shared)
	}
	agents := registry.AgentsOf(shared[0])
	if len(agents) != racers {
		t.Fatalf("Agents below the shared Window = %d, want %d", len(agents), racers)
	}
	uids := map[string]bool{}
	names := map[string]bool{}
	for _, out := range outs {
		uids[strings.TrimSpace(out)] = true
	}
	for _, agent := range agents {
		names[agent.Metadata.Name] = true
		if agent.Status.Phase != coremetadata.PhaseRunning {
			t.Fatalf("raced agent %q phase = %q, want Running", agent.Metadata.Name, agent.Status.Phase)
		}
	}
	if len(uids) != racers || len(names) != racers {
		t.Fatalf("distinct uids = %d, distinct names = %d, want %d of each", len(uids), len(names), racers)
	}
}

// TestADisabledProviderIsRefusedOnTheCanonicalRouteToo pins the Settings gate.
//
// `--force-agent` is a legacy compatibility flag and is not promoted here, so a
// disabled provider has exactly one remedy: enable it in Settings.
func TestADisabledProviderIsRefusedOnTheCanonicalRouteToo(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	before := store.snapshot()
	tmux := newFakeTmux()
	create, launcher := newTestAgentCreateCommand(t, store, tmux)
	launcher.disabled["codex"] = true

	stdout, _, err := runRoute(t, create, "agent", "--provider", "codex", "--project", "alpha", "--window", "review")
	if err == nil {
		t.Fatal("a disabled provider still created an Agent")
	}
	// The legacy handler classifies this as a plain error, so cmd/projmux exits
	// 1 rather than 2. Matching that classification is the contract; the message
	// differs only in not advertising --force-agent.
	if IsUsageError(err) {
		t.Fatalf("the disabled-provider refusal changed classification: %v", err)
	}
	if !strings.Contains(err.Error(), "disabled in Settings") {
		t.Fatalf("error = %q", err)
	}
	if stdout != "" || store.snapshot() != before || len(tmux.calls) != 0 {
		t.Fatalf("a disabled provider mutated something: stdout=%q tmux calls=%d", stdout, len(tmux.calls))
	}

	// The gate is the same one the legacy route applies; the canonical spelling
	// is not a bypass.
	if len(launcher.plans) != 0 {
		t.Fatal("a disabled provider still reached the launch construction")
	}
}

// TestCreateAgentHelpAdvertisesOnlyImplementedFlagsAndProjections is the
// help-honesty audit for the Agent node and its three shortcuts.
//
// Two earlier Phases shipped help that promised a capability the route did not
// have, so every advertised `-o` token is resolved and then run end to end, and
// every `--flag` in the usage synopsis has to be one the parser defines.
func TestCreateAgentHelpAdvertisesOnlyImplementedFlagsAndProjections(t *testing.T) {
	t.Parallel()

	create, ok := cli.LookupRoute("create")
	if !ok {
		t.Fatal("create route missing from the manifest")
	}

	for _, node := range append([]string{"agent"}, cli.ProviderCreateShortcuts()...) {
		t.Run(node, func(t *testing.T) {
			t.Parallel()

			var route cli.Route
			for _, child := range create.Children {
				if child.Name == node {
					route = child
				}
			}
			if route.Name == "" {
				t.Fatalf("create %s is not in the manifest", node)
			}
			if len(route.Outputs) == 0 {
				t.Fatalf("create %s advertises no output modes", node)
			}
			spelling := "create " + node

			args := []string{node, "--project", "alpha", "--window", "review"}
			if node == "agent" {
				args = append(args, "--provider", "codex")
			}
			for _, mode := range route.Outputs {
				if _, _, err := cli.ResolveOutputToken(spelling, string(mode)); err != nil {
					t.Fatalf("advertised mode %q does not resolve for %q: %v", mode, spelling, err)
				}
				store := newFakeResourceStore(t)
				cmd, _ := newTestAgentCreateCommand(t, store, newFakeTmux())
				run := append(append([]string(nil), args...), "-o", string(mode))
				if _, _, err := runRoute(t, cmd, run...); err != nil {
					t.Fatalf("advertised mode %q fails at runtime: %v", mode, err)
				}
			}

			// The usage synopsis names only flags the parser defines. The probe
			// mirrors the shape the Agent route registers.
			fs := flag.NewFlagSet("probe", flag.ContinueOnError)
			out := resourceCreateFlags{}
			fs.Var(&out.projects, "project", "")
			fs.String("provider", "", "")
			fs.Var(&out.windows, "window", "")
			fs.Var(&out.panes, "pane", "")
			fs.Var(&out.selectors, "selector", "")
			fs.Bool("create-window", false, "")
			fs.String("placement", "", "")
			fs.String("name", "", "")
			fs.Var(&out.labels, "label", "")
			fs.String("output", "", "")
			for _, usage := range route.Usage {
				for token := range strings.FieldsSeq(usage) {
					token = strings.Trim(token, "[]|.")
					if !strings.HasPrefix(token, "--") {
						continue
					}
					name := strings.TrimPrefix(token, "--")
					if name == "" {
						continue
					}
					if fs.Lookup(name) == nil {
						t.Fatalf("usage line %q advertises --%s, which the %s parser does not define", usage, name, spelling)
					}
				}
			}
		})
	}
}
