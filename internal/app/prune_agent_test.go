package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/core/selector"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

var pruneAgentStaleAt = resourceFixtureClock.Add(-1000 * time.Hour)

type pruneAgentFixtureAgent struct {
	uid        string
	name       string
	phase      coremetadata.AgentPhase
	at         time.Time
	reason     string
	sessionRef bool
	// panes are retained managed Pane uids. activated gives the single retained
	// Pane a complete activation, which is the shape Continue admits.
	panes     []string
	activated bool
}

// addPruneAgentFixture appends one Agent, its managed Panes, and their name
// reservations under windowUID of projectUID.
func addPruneAgentFixture(registry *coremetadata.Registry, projectUID, windowUID string, spec pruneAgentFixtureAgent) {
	agent := coremetadata.Agent{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindAgent,
		Metadata: coremetadata.ObjectMeta{UID: spec.uid, Name: spec.name, OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindWindow, UID: windowUID}, CreatedAt: pruneAgentStaleAt},
		Spec:     coremetadata.AgentSpec{Provider: "codex"},
		Status:   coremetadata.AgentStatus{Phase: spec.phase, LastTransitionAt: spec.at, Reason: spec.reason},
	}
	if spec.sessionRef {
		agent.Status.SessionRef = &coremetadata.AgentSessionRef{
			Provider: "codex", ObservedAt: pruneAgentStaleAt,
			Codex: &coremetadata.CodexSessionRef{ThreadID: "thread-" + spec.name},
		}
	}
	registry.Agents = append(registry.Agents, agent)
	registry.NameReservations = append(registry.NameReservations, coremetadata.NameReservation{
		Scope: projectUID, Kind: coremetadata.KindAgent, Name: spec.name, UID: spec.uid,
	})
	for _, paneUID := range spec.panes {
		pane := coremetadata.Pane{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
			Metadata: coremetadata.ObjectMeta{UID: paneUID, Name: paneUID, OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindAgent, UID: spec.uid}, CreatedAt: pruneAgentStaleAt},
			Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleAgent, CWD: "/srv/beta"},
		}
		if spec.activated {
			pane.Status.Activation = coremetadata.PaneActivation{
				Generation: "gen-" + spec.name, AgentUID: spec.uid, OperationID: "op-" + spec.name, StartedAt: pruneAgentStaleAt,
			}
		}
		registry.Panes = append(registry.Panes, pane)
		registry.NameReservations = append(registry.NameReservations, coremetadata.NameReservation{
			Scope: projectUID, Kind: coremetadata.KindPane, Name: paneUID, UID: paneUID,
		})
	}
}

// pruneAgentFixtureRegistry extends the shared resource fixture with one Agent
// per selection rule.
//
//   - agt-alpha-codex: Running and old, with a managed Pane (phase excludes it).
//   - agt-beta-codex: Offline, old, no ref, no Pane (always a candidate).
//   - agt-stale-failed: Failed, old, no ref, no Pane (always a candidate).
//   - agt-live-pane: Offline, old, no ref, one Pane the inventory reports live.
//   - agt-dead-pane: Offline, old, no ref, one offline Pane Continue admits.
//   - agt-kept: Offline, old, no ref, no Pane; the --exclude target.
//   - agt-young: Offline, no ref, no Pane, but transitioned an hour ago.
//   - agt-has-ref: Offline, old, no Pane, but records a session ref.
func pruneAgentFixtureRegistry(t *testing.T) coremetadata.Registry {
	t.Helper()
	registry := resourceFixtureRegistry(t)
	for i := range registry.Agents {
		registry.Agents[i].Status.LastTransitionAt = pruneAgentStaleAt
	}
	for _, spec := range []pruneAgentFixtureAgent{
		{uid: "agt-stale-failed", name: "stale-failed", phase: coremetadata.PhaseFailed, at: pruneAgentStaleAt, reason: "ProcessExited"},
		{uid: "agt-live-pane", name: "live-pane", phase: coremetadata.PhaseOffline, at: pruneAgentStaleAt, panes: []string{"pan-live-pane"}},
		{uid: "agt-dead-pane", name: "dead-pane", phase: coremetadata.PhaseOffline, at: pruneAgentStaleAt, panes: []string{"pan-dead-pane"}, activated: true},
		{uid: "agt-kept", name: "kept", phase: coremetadata.PhaseOffline, at: pruneAgentStaleAt},
		{uid: "agt-young", name: "young", phase: coremetadata.PhaseOffline, at: resourceFixtureClock.Add(-time.Hour)},
		{uid: "agt-has-ref", name: "has-ref", phase: coremetadata.PhaseOffline, at: pruneAgentStaleAt, sessionRef: true},
	} {
		addPruneAgentFixture(&registry, "prj-beta", "win-beta-main", spec)
	}
	registry = registry.Normalize()
	if err := registry.Validate(); err != nil {
		t.Fatalf("prune agent fixture is invalid: %v", err)
	}
	return registry
}

// pruneAgentLiveInventory is a readable exact-server observation in which only
// the named Pane uids are mirrored by live tmux panes.
func pruneAgentLiveInventory(liveUIDs ...string) resourcegraph.Inventory {
	inventory := resourcegraph.Inventory{
		Transport: resourcegraph.Transport{Kind: resourcegraph.TransportSocketName, Value: "projmux", Source: resourcegraph.TransportSourceSocketName},
		HostMode:  resourcegraph.HostModeAppOwned,
	}
	for i, uid := range liveUIDs {
		inventory.Panes = append(inventory.Panes, resourcegraph.Pane{ID: fmt.Sprintf("%%%d", 70+i), WindowID: "@7", UID: uid})
	}
	return inventory
}

type pruneAgentObserverProbe struct {
	calls       int
	inventories []resourcegraph.Inventory
}

// observe answers the i-th call with the i-th inventory, repeating the last.
func (p *pruneAgentObserverProbe) observe(context.Context) resourcegraph.Inventory {
	p.calls++
	index := min(p.calls, len(p.inventories)) - 1
	return p.inventories[index].Clone()
}

func newPruneAgentFakeStore(t *testing.T) *fakeResourceStore {
	t.Helper()
	store := newFakeResourceStore(t)
	store.registry = pruneAgentFixtureRegistry(t)
	return store
}

func newTestPruneAgentCommand(store *resourceStore, now func() time.Time, inventories ...resourcegraph.Inventory) (*pruneAgentCommand, *pruneAgentObserverProbe) {
	if len(inventories) == 0 {
		inventories = []resourcegraph.Inventory{pruneAgentLiveInventory("pan-live-pane")}
	}
	probe := &pruneAgentObserverProbe{inventories: inventories}
	return &pruneAgentCommand{store: store, now: now, observe: probe.observe}, probe
}

var pruneAgentRowUID = regexp.MustCompile(`(?m)^  (?:unknown )?agent/\S+ uid=(\S+) `)

// listedPruneAgentUIDs returns the uids of every listed row, sorted.
func listedPruneAgentUIDs(stdout string) []string {
	var uids []string
	for _, match := range pruneAgentRowUID.FindAllStringSubmatch(stdout, -1) {
		uids = append(uids, match[1])
	}
	sort.Strings(uids)
	return uids
}

// pruneAgentDiskStore is a real registry file in a temp state directory.
type pruneAgentDiskStore struct {
	stateDir string
	store    *intmetadata.Store
}

func newPruneAgentDiskStore(t *testing.T, registry coremetadata.Registry) *pruneAgentDiskStore {
	t.Helper()
	stateDir := t.TempDir()
	store := intmetadata.NewStore(intmetadata.PathFor(stateDir))
	store.SetClock(func() time.Time { return resourceFixtureClock })
	if _, err := store.Update(func(working *coremetadata.Registry) error {
		*working = registry.Clone()
		return nil
	}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	return &pruneAgentDiskStore{stateDir: stateDir, store: store}
}

// pruneAgentTestMutator is deterministic, and every call starts its own uid
// sequence, so a clone replay and the route allocate identical uids.
func pruneAgentTestMutator() coremetadata.Mutator {
	next := 0
	return coremetadata.Mutator{
		Now:       func() time.Time { return resourceFixtureClock },
		DirExists: func(string) (bool, error) { return true, nil },
		NewUID: func(kind coremetadata.Kind) (string, error) {
			next++
			return fmt.Sprintf("%s-prune-%d", strings.ToLower(string(kind)), next), nil
		},
	}
}

func (d *pruneAgentDiskStore) resourceStore() *resourceStore {
	return &resourceStore{
		load:    d.store.LoadDegradedReadOnly,
		update:  d.store.Update,
		mutator: pruneAgentTestMutator,
	}
}

// tree fingerprints every entry under the state directory: path, mode, size,
// modification time, and content hash. Equal trees prove no write, no temp
// file, and no lock-induced rewrite.
func (d *pruneAgentDiskStore) tree(t *testing.T) string {
	t.Helper()
	var entries []string
	err := filepath.WalkDir(d.stateDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(d.stateDir, path)
		line := fmt.Sprintf("%s mode=%s size=%d mtime=%d", rel, info.Mode(), info.Size(), info.ModTime().UnixNano())
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			line += fmt.Sprintf(" sha256=%x", sha256.Sum256(data))
		}
		entries = append(entries, line)
		return nil
	})
	if err != nil {
		t.Fatalf("walk state dir: %v", err)
	}
	return strings.Join(entries, "\n")
}

func (d *pruneAgentDiskStore) registrySHA(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(d.store.Path())
	if err != nil {
		t.Fatalf("read registry.json: %v", err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func fixedPruneAgentClock() time.Time { return resourceFixtureClock }

// TestPruneAgentDryRunLeavesRegistryBytesUntouched is acceptance criteria 1
// and 7: the default listing prints every column and the dry-run line, and
// registry.json keeps its sha256 with no transaction, temp file, or lock file
// created beside it.
func TestPruneAgentDryRunLeavesRegistryBytesUntouched(t *testing.T) {
	t.Parallel()

	disk := newPruneAgentDiskStore(t, pruneAgentFixtureRegistry(t))
	beforeSHA, beforeTree := disk.registrySHA(t), disk.tree(t)
	cmd, _ := newTestPruneAgentCommand(disk.resourceStore(), fixedPruneAgentClock)
	stdout, stderr, err := runRoute(t, cmd, "--older-than", "720h", "--no-session-ref")
	if err != nil {
		t.Fatalf("prune agent dry-run error = %v (stderr=%s)", err, stderr)
	}
	for _, want := range []string{
		"prune agent: would delete 4 agents\n",
		"  agent/codex uid=agt-beta-codex provider=codex phase=Offline reason=- lastTransitionAt=2026-07-04T17:00:00Z sessionRef=no panes=0 continue=no\n",
		"  agent/kept uid=agt-kept provider=codex phase=Offline reason=- lastTransitionAt=2026-07-04T17:00:00Z sessionRef=no panes=0 continue=no\n",
		"  agent/stale-failed uid=agt-stale-failed provider=codex phase=Failed reason=ProcessExited lastTransitionAt=2026-07-04T17:00:00Z sessionRef=no panes=0 continue=no\n",
		"  agent/dead-pane uid=agt-dead-pane provider=codex phase=Offline reason=- lastTransitionAt=2026-07-04T17:00:00Z sessionRef=no panes=1 continue=yes\n",
		"dry-run: nothing was deleted; re-run with --yes to delete\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("listing is missing %q:\n%s", want, stdout)
		}
	}
	if got := disk.registrySHA(t); got != beforeSHA {
		t.Fatalf("registry.json sha256 changed by a dry-run: %s -> %s", beforeSHA, got)
	}
	if got := disk.tree(t); got != beforeTree {
		t.Fatalf("the state directory changed during a dry-run:\n--- before ---\n%s\n--- after ---\n%s", beforeTree, got)
	}
}

// TestPruneAgentNeverSelectsRunningLiveExcludedOrYoungAgents is acceptance
// criteria 2, 3, and 5's exclusion half, iterated over every accepted evidence
// flag combination, listing and deleting alike.
func TestPruneAgentNeverSelectsRunningLiveExcludedOrYoungAgents(t *testing.T) {
	t.Parallel()

	never := []string{"agt-alpha-codex", "agt-live-pane", "agt-kept", "agt-young"}
	for _, combo := range []struct {
		name  string
		flags []string
		want  []string
	}{
		{"no-session-ref", []string{"--no-session-ref"}, []string{"agt-beta-codex", "agt-dead-pane", "agt-stale-failed"}},
		{"no-pane", []string{"--no-pane"}, []string{"agt-beta-codex", "agt-has-ref", "agt-stale-failed"}},
		{"no-session-ref and no-pane", []string{"--no-session-ref", "--no-pane"}, []string{"agt-beta-codex", "agt-stale-failed"}},
	} {
		for _, yes := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s yes=%t", combo.name, yes), func(t *testing.T) {
				t.Parallel()
				store := newPruneAgentFakeStore(t)
				args := append([]string{"--older-than", "720h", "--exclude", "uid:agt-kept"}, combo.flags...)
				if yes {
					args = append(args, "--yes")
				}
				cmd, _ := newTestPruneAgentCommand(store.store(), fixedPruneAgentClock)
				stdout, _, err := runRoute(t, cmd, args...)
				if err != nil {
					t.Fatalf("prune agent %v error = %v", args, err)
				}
				if got := listedPruneAgentUIDs(stdout); !reflect.DeepEqual(got, combo.want) {
					t.Fatalf("prune agent %v listed %v, want %v:\n%s", args, got, combo.want, stdout)
				}
				for _, uid := range never {
					if _, ok := store.registry.Agent(uid); !ok {
						t.Fatalf("prune agent %v removed %s, which must never be a candidate", args, uid)
					}
				}
				for _, uid := range combo.want {
					_, ok := store.registry.Agent(uid)
					if yes == ok {
						t.Fatalf("prune agent %v: candidate %s present=%t after yes=%t", args, uid, ok, yes)
					}
				}
			})
		}
	}

	// The controls prove each rule is what excluded its Agent: without the
	// exclusion the kept Agent is a candidate, and a shorter bound admits the
	// young one, so neither was dropped by some other criterion.
	t.Run("controls", func(t *testing.T) {
		t.Parallel()
		store := newPruneAgentFakeStore(t)
		cmd, _ := newTestPruneAgentCommand(store.store(), fixedPruneAgentClock)
		stdout, _, err := runRoute(t, cmd, "--older-than", "30m", "--no-pane")
		if err != nil {
			t.Fatal(err)
		}
		got := listedPruneAgentUIDs(stdout)
		for _, uid := range []string{"agt-kept", "agt-young"} {
			if !slices.Contains(got, uid) {
				t.Fatalf("control %s is not a candidate without its excluding rule: %v", uid, got)
			}
		}
		// The live Pane's Agent is a candidate once the same Pane is observed
		// offline, so liveness alone excluded it above.
		cmd, _ = newTestPruneAgentCommand(store.store(), fixedPruneAgentClock, pruneAgentLiveInventory())
		stdout, _, err = runRoute(t, cmd, "--older-than", "720h", "--no-session-ref")
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(listedPruneAgentUIDs(stdout), "agt-live-pane") {
			t.Fatalf("an offline-observed Pane still excluded its Agent:\n%s", stdout)
		}
		// A Running Agent stays out even when it has no session ref and its Pane
		// is not live: phase alone decides.
		if slices.Contains(listedPruneAgentUIDs(stdout), "agt-alpha-codex") {
			t.Fatalf("a Running Agent was listed:\n%s", stdout)
		}
	})
}

// TestPruneAgentUsageErrorsChangeNothing is acceptance criteria 2 and 5's
// usage half: every malformed invocation is a usage error with zero stdout,
// zero transactions, and an unchanged registry.
func TestPruneAgentUsageErrorsChangeNothing(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"no evidence flag", []string{"--older-than", "720h"}, "requires --no-session-ref, --no-pane, or both"},
		{"missing older-than", []string{"--no-pane"}, "requires --older-than"},
		{"negative older-than", []string{"--no-pane", "--older-than", "-1h"}, "must not be negative"},
		{"malformed older-than", []string{"--no-pane", "--older-than", "forever"}, "is not a duration"},
		{"positional argument", []string{"--no-pane", "--older-than", "1h", "kept"}, "does not accept positional arguments"},
		{"unresolvable exclude uid", []string{"--no-pane", "--older-than", "1h", "--exclude", "uid:agt-nope"}, "--exclude \"uid:agt-nope\""},
		{"unresolvable exclude name", []string{"--no-pane", "--older-than", "1h", "--exclude", "nope"}, "--exclude \"nope\""},
		{"empty exclude", []string{"--no-pane", "--older-than", "1h", "--exclude", ""}, "--exclude \"\""},
		{"ambiguous exclude name", []string{"--no-pane", "--older-than", "1h", "--exclude", "codex"}, "matched 2 Agents"},
		{"unresolvable exclude with a valid one", []string{"--no-pane", "--older-than", "1h", "--exclude", "kept", "--exclude", "uid:agt-nope"}, "--exclude \"uid:agt-nope\""},
		{"unknown flag", []string{"--no-pane", "--older-than", "1h", "--all"}, "flag provided but not defined"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newPruneAgentFakeStore(t)
			before := store.snapshot()
			cmd, probe := newTestPruneAgentCommand(store.store(), fixedPruneAgentClock)
			stdout, _, err := runRoute(t, cmd, test.args...)
			if err == nil {
				t.Fatalf("prune agent %v succeeded:\n%s", test.args, stdout)
			}
			if !IsUsageError(err) {
				t.Fatalf("prune agent %v error is not a usage error: %v", test.args, err)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("prune agent %v error = %q, want it to mention %q", test.args, err, test.want)
			}
			if stdout != "" {
				t.Fatalf("prune agent %v wrote %q to stdout, want 0 bytes", test.args, stdout)
			}
			if store.transactions != 0 || store.writes != 0 || store.snapshot() != before {
				t.Fatalf("prune agent %v touched the registry: transactions=%d writes=%d", test.args, store.transactions, store.writes)
			}
			if probe.calls != 0 {
				t.Fatalf("prune agent %v observed tmux %d times before refusing", test.args, probe.calls)
			}
		})
	}
}

// TestPruneAgentExcludeUsesTheSharedAgentSelector keeps --exclude on the
// ordinary Agent reference grammar: an exact name and a uid: reference both
// resolve, registry-wide.
func TestPruneAgentExcludeUsesTheSharedAgentSelector(t *testing.T) {
	t.Parallel()

	for _, ref := range []string{"kept", "uid:agt-kept"} {
		store := newPruneAgentFakeStore(t)
		cmd, _ := newTestPruneAgentCommand(store.store(), fixedPruneAgentClock)
		stdout, _, err := runRoute(t, cmd, "--older-than", "720h", "--no-pane", "--exclude", ref, "--exclude", "uid:agt-stale-failed")
		if err != nil {
			t.Fatalf("--exclude %s error = %v", ref, err)
		}
		got := listedPruneAgentUIDs(stdout)
		if slices.Contains(got, "agt-kept") || slices.Contains(got, "agt-stale-failed") {
			t.Fatalf("--exclude %s kept an excluded Agent in the candidates: %v", ref, got)
		}
		if !slices.Contains(got, "agt-beta-codex") {
			t.Fatalf("--exclude %s dropped an unrelated candidate: %v", ref, got)
		}
	}
}

// TestPruneAgentYesRefusesWhenLockedReselectionDiffers is acceptance criterion
// 6's refusal half: any difference between the listed plan and the plan
// re-selected inside the lock refuses the whole run with zero writes.
func TestPruneAgentYesRefusesWhenLockedReselectionDiffers(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		// inject edits the stored registry after the listing read.
		inject      func(*coremetadata.Registry)
		clock       []time.Time
		inventories []resourcegraph.Inventory
		want        string
	}{
		{
			name: "a candidate recorded a session ref after the listing",
			args: []string{"--older-than", "720h", "--no-session-ref"},
			inject: func(registry *coremetadata.Registry) {
				agent, _ := registry.Agent("agt-stale-failed")
				agent.Status.SessionRef = &coremetadata.AgentSessionRef{Provider: "codex", ObservedAt: resourceFixtureClock, Codex: &coremetadata.CodexSessionRef{ThreadID: "late"}}
			},
			want: "candidate set changed",
		},
		{
			name: "a candidate's phase and reason changed after the listing",
			args: []string{"--older-than", "720h", "--no-pane"},
			inject: func(registry *coremetadata.Registry) {
				agent, _ := registry.Agent("agt-beta-codex")
				agent.Status.Phase = coremetadata.PhaseFailed
				agent.Status.Reason = "changed"
			},
			want: "candidate set changed",
		},
		{
			name: "an Agent crossed the age bound in the stored registry",
			args: []string{"--older-than", "720h", "--no-pane"},
			inject: func(registry *coremetadata.Registry) {
				agent, _ := registry.Agent("agt-young")
				agent.Status.LastTransitionAt = pruneAgentStaleAt
			},
			want: "candidate set changed",
		},
		{
			name:  "the clock moved an Agent across the age bound",
			args:  []string{"--older-than", "90m", "--no-pane"},
			clock: []time.Time{resourceFixtureClock, resourceFixtureClock.Add(time.Hour)},
			want:  "candidate set changed",
		},
		{
			name:        "a remaining Pane came back live",
			args:        []string{"--older-than", "720h", "--no-session-ref"},
			inventories: []resourcegraph.Inventory{pruneAgentLiveInventory("pan-live-pane"), pruneAgentLiveInventory("pan-live-pane", "pan-dead-pane")},
			want:        "candidate set changed",
		},
		{
			name:        "the live observation became unavailable",
			args:        []string{"--older-than", "720h", "--no-session-ref"},
			inventories: []resourcegraph.Inventory{pruneAgentLiveInventory("pan-live-pane"), unavailablePruneAgentInventory("tmux panes could not be listed: boom")},
			want:        "live tmux observation is unavailable",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newPruneAgentFakeStore(t)
			resources := store.store()
			load := resources.load
			want := store.snapshot()
			resources.load = func() (coremetadata.Registry, error) {
				registry, err := load()
				if test.inject != nil {
					test.inject(&store.registry)
					want = store.snapshot()
				}
				return registry, err
			}
			calls := 0
			now := func() time.Time {
				calls++
				if len(test.clock) == 0 {
					return resourceFixtureClock
				}
				return test.clock[min(calls, len(test.clock))-1]
			}
			cmd, _ := newTestPruneAgentCommand(resources, now, test.inventories...)
			stdout, _, err := runRoute(t, cmd, append(test.args, "--yes")...)
			if err == nil {
				t.Fatalf("prune agent --yes succeeded on a changed plan:\n%s", stdout)
			}
			if !strings.Contains(err.Error(), "nothing was deleted") || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %q, want %q and `nothing was deleted`", err, test.want)
			}
			if store.writes != 0 || store.snapshot() != want {
				t.Fatalf("a refused prune changed the registry: writes=%d", store.writes)
			}
			if stdout != "" {
				t.Fatalf("a refused prune wrote %q", stdout)
			}
		})
	}
}

// TestPruneAgentYesMatchesMutatorDeleteAgentOnAClone is acceptance criterion
// 6's execution half: the committed registry is exactly Mutator.DeleteAgent
// applied to a clone -- rows, Panes, name reservations, and Window anchor
// repair -- and nothing outside registry.json is written.
func TestPruneAgentYesMatchesMutatorDeleteAgentOnAClone(t *testing.T) {
	t.Parallel()

	registry := pruneAgentFixtureRegistry(t)
	// An Agent-only Window: deleting its Agent leaves the Window with no
	// descendant, so DeleteAgent's anchor repair has to allocate a shell.
	registry.Windows = append(registry.Windows, coremetadata.Window{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
		Metadata: coremetadata.ObjectMeta{UID: "win-beta-agents", Name: "agents", OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindProject, UID: "prj-beta"}, CreatedAt: pruneAgentStaleAt},
		Spec:     coremetadata.WindowSpec{AnchorPaneRef: "pan-solo"},
	})
	registry.NameReservations = append(registry.NameReservations, coremetadata.NameReservation{
		Scope: "prj-beta", Kind: coremetadata.KindWindow, Name: "agents", UID: "win-beta-agents",
	})
	addPruneAgentFixture(&registry, "prj-beta", "win-beta-agents", pruneAgentFixtureAgent{
		uid: "agt-solo", name: "solo", phase: coremetadata.PhaseOffline, at: pruneAgentStaleAt, panes: []string{"pan-solo"},
	})
	// A Window anchored on an Agent Pane requires that Pane to be the Agent's
	// current binding.
	solo, _ := registry.Agent("agt-solo")
	solo.Status.PaneRef = "pan-solo"
	registry = registry.Normalize()
	if err := registry.Validate(); err != nil {
		t.Fatalf("fixture invalid: %v", err)
	}

	disk := newPruneAgentDiskStore(t, registry)
	receipts := filepath.Join(disk.stateDir, terminationJournalFile)
	receiptBytes := []byte(`{"paneUID":"pan-dead-pane","source":"supervisor"}` + "\n")
	if err := os.WriteFile(receipts, receiptBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	seeded, err := disk.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	candidates := []string{"agt-beta-codex", "agt-stale-failed", "agt-dead-pane", "agt-solo"}
	want := seeded.Clone()
	mutator := pruneAgentTestMutator()
	for _, uid := range candidates {
		if err := mutator.DeleteAgent(&want, uid); err != nil {
			t.Fatalf("clone DeleteAgent(%s): %v", uid, err)
		}
	}
	want = want.Normalize()

	cmd, _ := newTestPruneAgentCommand(disk.resourceStore(), fixedPruneAgentClock)
	stdout, stderr, err := runRoute(t, cmd, "--older-than", "720h", "--no-session-ref", "--exclude", "kept", "--yes")
	if err != nil {
		t.Fatalf("prune agent --yes error = %v (stderr=%s)", err, stderr)
	}
	if got := listedPruneAgentUIDs(stdout); !reflect.DeepEqual(got, []string{"agt-beta-codex", "agt-dead-pane", "agt-solo", "agt-stale-failed"}) {
		t.Fatalf("deleted rows = %v:\n%s", got, stdout)
	}
	if !strings.Contains(stdout, "prune agent: deleted 4 agents\n") || strings.Contains(stdout, "dry-run") {
		t.Fatalf("stdout = %q", stdout)
	}
	got, err := disk.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("committed registry differs from Mutator.DeleteAgent on a clone:\n got=%+v\nwant=%+v", got, want)
	}
	for _, uid := range candidates {
		if _, ok := got.Agent(uid); ok {
			t.Fatalf("candidate Agent %s survived", uid)
		}
		if panes := got.PanesOf(uid); len(panes) != 0 {
			t.Fatalf("candidate Agent %s kept Panes %v", uid, panes)
		}
		for _, reservation := range got.NameReservations {
			if reservation.UID == uid {
				t.Fatalf("candidate Agent %s kept name reservation %+v", uid, reservation)
			}
		}
	}
	for _, paneUID := range []string{"pan-dead-pane", "pan-solo"} {
		if _, ok := got.Pane(paneUID); ok {
			t.Fatalf("candidate Pane %s survived", paneUID)
		}
		for _, reservation := range got.NameReservations {
			if reservation.UID == paneUID {
				t.Fatalf("candidate Pane %s kept name reservation %+v", paneUID, reservation)
			}
		}
	}
	window, ok := got.Window("win-beta-agents")
	if !ok || window.Spec.AnchorPaneRef == "" || window.Spec.AnchorPaneRef == "pan-solo" {
		t.Fatalf("Agent-only Window anchor was not repaired: %+v", window)
	}
	for _, uid := range []string{"agt-alpha-codex", "agt-live-pane", "agt-kept", "agt-young", "agt-has-ref"} {
		if _, ok := got.Agent(uid); !ok {
			t.Fatalf("non-candidate Agent %s was deleted", uid)
		}
	}
	after, err := os.ReadFile(receipts)
	if err != nil || string(after) != string(receiptBytes) {
		t.Fatalf("termination-receipts.jsonl changed: %q (err=%v)", after, err)
	}
}

// TestPruneAgentContinueColumnComesFromTopologyEligibility is acceptance
// criterion 4's Continue column: it is exactly the existing eligibility
// verdict, yes for an admitted retained activation and no otherwise.
func TestPruneAgentContinueColumnComesFromTopologyEligibility(t *testing.T) {
	t.Parallel()

	store := newPruneAgentFakeStore(t)
	for uid, want := range map[string]bool{"agt-dead-pane": true, "agt-stale-failed": false, "agt-beta-codex": false} {
		agent, _ := store.registry.Agent(uid)
		if got, _, reason := decideTopologyAgentContinueEligibility(store.registry, agent.Clone()); got != want {
			t.Fatalf("fixture %s eligibility = %t (%s), want %t", uid, got, reason, want)
		}
	}
	cmd, _ := newTestPruneAgentCommand(store.store(), fixedPruneAgentClock)
	stdout, _, err := runRoute(t, cmd, "--older-than", "720h", "--no-session-ref")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"agent/dead-pane uid=agt-dead-pane ",
		"agent/stale-failed uid=agt-stale-failed ",
		"agent/codex uid=agt-beta-codex ",
	} {
		line := ""
		for row := range strings.SplitSeq(stdout, "\n") {
			if strings.Contains(row, want) {
				line = row
			}
		}
		wantColumn := " continue=no"
		if strings.Contains(want, "dead-pane") {
			wantColumn = " continue=yes"
		}
		if !strings.HasSuffix(line, wantColumn) {
			t.Fatalf("row %q does not end with %q:\n%s", want, wantColumn, stdout)
		}
	}
}

type pruneAgentTmuxReply struct {
	out string
	err error
}

// pruneAgentTmuxRunner scripts the exact-socket inventory commands by tmux
// subcommand; an unscripted subcommand answers empty output.
type pruneAgentTmuxRunner struct {
	replies map[string]pruneAgentTmuxReply
	calls   []string
}

func (r *pruneAgentTmuxRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, strings.Join(args, " "))
	for _, arg := range args {
		if reply, ok := r.replies[arg]; ok {
			return []byte(reply.out), reply.err
		}
	}
	return nil, nil
}

// pruneAgentMirroredPaneRow is one nine-field `list-panes -a` row whose pane
// mirrors uid.
func pruneAgentMirroredPaneRow(uid string) string {
	return strings.Join([]string{"%70", "@7", uid, "", "", "", "", "", ""}, "\x1f") + "\n"
}

func pruneAgentReaderObservation(runner *pruneAgentTmuxRunner, tmuxEnv string) pruneAgentObservation {
	reader := newRuntimeDiagnosticsReader(runner)
	reader.lookupEnv = func(key string) string {
		if key == "TMUX" {
			return tmuxEnv
		}
		return ""
	}
	return runtimeReaderObservation(reader)
}

// TestPruneAgentLivenessRequiresAnObservedAppOwnedHost is acceptance criterion
// 9 and the fail-closed half of criterion 3. "No live Pane" is admitted only
// from an actual read of a projmux app-owned host. Each observed case runs the
// real runtime reader and InventoryObserver over a scripted tmux runner, so the
// scope and host-mode classification is inventory.go's own; the fallback cases
// hand the route a Registry-only snapshot or no observer at all. Every case
// that is not an app-owned host lists Pane-holding matches as unknown with the
// reason, and --yes refuses with the registry file unchanged, while Pane-free
// matches are still judged from the Registry alone.
func TestPruneAgentLivenessRequiresAnObservedAppOwnedHost(t *testing.T) {
	t.Parallel()

	const socket = "/tmp/projmux-prune-test/sock"
	appOwned := pruneAgentTmuxReply{out: "1\n"}
	for _, test := range []struct {
		name    string
		tmuxEnv string
		replies map[string]pruneAgentTmuxReply
		// fallback replaces the reader with a non-observing source.
		fallback     func() pruneAgentObservation
		reason       string
		wantCalls    bool
		liveMirrored bool
		appOwned     bool
	}{
		{name: "no transport outside tmux", tmuxEnv: "", replies: map[string]pruneAgentTmuxReply{"show-options": appOwned}, reason: "names no exact tmux server"},
		{
			name: "Registry-only fallback snapshot",
			fallback: func() pruneAgentObservation {
				return func(context.Context) resourcegraph.Inventory { return resourcegraph.Inventory{} }
			},
			reason: "names no exact tmux server",
		},
		{name: "no observer configured", fallback: func() pruneAgentObservation { return nil }, reason: "live tmux observer is not configured"},
		{
			name: "standalone server without the @projmux_app marker", tmuxEnv: socket + ",1,0",
			replies: map[string]pruneAgentTmuxReply{
				"show-options": {err: errors.New("tmux: invalid option: @projmux_app")},
				"list-panes":   {out: pruneAgentMirroredPaneRow("pan-live-pane")},
			},
			reason: "is not projmux app-owned (no @projmux_app marker)", wantCalls: true, liveMirrored: true,
		},
		{
			name: "standalone server with a non-app marker value", tmuxEnv: socket + ",1,0",
			replies: map[string]pruneAgentTmuxReply{"show-options": {out: "0\n"}},
			reason:  "is not projmux app-owned (no @projmux_app marker)", wantCalls: true,
		},
		{
			name: "no server running on the socket", tmuxEnv: socket + ",1,0",
			replies: map[string]pruneAgentTmuxReply{"show-options": {err: errors.New("no server running on " + socket)}},
			reason:  "no tmux server on", wantCalls: true,
		},
		{
			name: "Pane list failure on an app-owned host", tmuxEnv: socket + ",1,0",
			replies: map[string]pruneAgentTmuxReply{"show-options": appOwned, "list-panes": {err: errors.New("tmux: boom")}},
			reason:  "tmux panes could not be listed", wantCalls: true,
		},
		{
			name: "app-owned host with an empty Pane scope", tmuxEnv: socket + ",1,0",
			replies:   map[string]pruneAgentTmuxReply{"show-options": appOwned},
			wantCalls: true, appOwned: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			disk := newPruneAgentDiskStore(t, pruneAgentFixtureRegistry(t))
			beforeSHA, beforeTree := disk.registrySHA(t), disk.tree(t)
			runner := &pruneAgentTmuxRunner{replies: test.replies}
			observe := pruneAgentReaderObservation(runner, test.tmuxEnv)
			if test.fallback != nil {
				observe = test.fallback()
			}
			cmd := &pruneAgentCommand{store: disk.resourceStore(), now: fixedPruneAgentClock, observe: observe}
			args := []string{"--older-than", "720h", "--no-session-ref"}

			stdout, _, err := runRoute(t, cmd, args...)
			if err != nil {
				t.Fatalf("dry-run error = %v", err)
			}
			if (len(runner.calls) > 0) != test.wantCalls {
				t.Fatalf("tmux calls = %v, want calls=%t", runner.calls, test.wantCalls)
			}
			// A Pane-free match needs no observation and is a candidate in
			// every case.
			for _, want := range []string{"  agent/stale-failed uid=agt-stale-failed ", "dry-run: nothing was deleted"} {
				if !strings.Contains(stdout, want) {
					t.Fatalf("dry-run is missing %q:\n%s", want, stdout)
				}
			}

			if test.appOwned {
				if strings.Contains(stdout, "unknown") {
					t.Fatalf("an app-owned host with an empty Pane scope was reported unknown:\n%s", stdout)
				}
				for _, want := range []string{"  agent/dead-pane uid=agt-dead-pane ", "  agent/live-pane uid=agt-live-pane "} {
					if !strings.Contains(stdout, want) {
						t.Fatalf("an offline Pane on an app-owned host did not make its Agent a candidate %q:\n%s", want, stdout)
					}
				}
				if got := disk.registrySHA(t); got != beforeSHA {
					t.Fatal("the dry-run changed registry.json")
				}
				if _, _, err := runRoute(t, cmd, append(args, "--yes")...); err != nil {
					t.Fatalf("--yes on an app-owned host error = %v", err)
				}
				after, err := disk.store.LoadReadOnly()
				if err != nil {
					t.Fatal(err)
				}
				if _, ok := after.Agent("agt-dead-pane"); ok {
					t.Fatal("an Agent whose Pane is offline on an app-owned host survived --yes")
				}
				return
			}

			for _, want := range []string{
				"  unknown agent/dead-pane uid=agt-dead-pane ",
				" live=unknown\n",
				"live tmux observation unavailable: ",
				test.reason,
				"--yes is refused",
			} {
				if !strings.Contains(stdout, want) {
					t.Fatalf("dry-run is missing %q:\n%s", want, stdout)
				}
			}
			// A uid mirrored on the observed server is still live evidence, and
			// it is judged before unknown.
			liveListed := strings.Contains(stdout, "agent/live-pane uid=agt-live-pane ")
			if liveListed == test.liveMirrored {
				t.Fatalf("live-pane listed=%t with its Pane mirrored=%t:\n%s", liveListed, test.liveMirrored, stdout)
			}
			if strings.Contains(stdout, "  agent/dead-pane uid=agt-dead-pane ") {
				t.Fatalf("an unobserved Pane was read as offline:\n%s", stdout)
			}

			stdout, _, err = runRoute(t, cmd, append(args, "--yes")...)
			if err == nil || !strings.Contains(err.Error(), "nothing was deleted") || !strings.Contains(err.Error(), test.reason) {
				t.Fatalf("--yes error = %v, want a refusal naming %q and `nothing was deleted`", err, test.reason)
			}
			if stdout != "" {
				t.Fatalf("refused --yes wrote %q", stdout)
			}
			if got := disk.registrySHA(t); got != beforeSHA {
				t.Fatalf("refused --yes changed registry.json sha256: %s -> %s", beforeSHA, got)
			}
			if got := disk.tree(t); got != beforeTree {
				t.Fatalf("refused --yes changed the state directory:\n--- before ---\n%s\n--- after ---\n%s", beforeTree, got)
			}
		})
	}
}

// TestPruneAgentWithoutRemainingPanesNeedsNoTmux keeps the observation lazy:
// Agents with no remaining Pane are judged from the Registry alone.
func TestPruneAgentWithoutRemainingPanesNeedsNoTmux(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"--older-than", "720h", "--no-pane"},
		{"--older-than", "720h", "--no-pane", "--yes"},
		{"--older-than", "720h", "--no-session-ref", "--no-pane", "--yes"},
		{"--older-than", "720h", "--no-session-ref", "--exclude", "dead-pane", "--exclude", "live-pane"},
	} {
		store := newPruneAgentFakeStore(t)
		cmd, probe := newTestPruneAgentCommand(store.store(), fixedPruneAgentClock)
		if _, _, err := runRoute(t, cmd, args...); err != nil {
			t.Fatalf("prune agent %v error = %v", args, err)
		}
		if probe.calls != 0 {
			t.Fatalf("prune agent %v observed tmux %d times with no Pane-holding match", args, probe.calls)
		}
	}
}

// TestPruneAgentCandidateListingIsBounded is acceptance criterion 4's bound.
func TestPruneAgentCandidateListingIsBounded(t *testing.T) {
	t.Parallel()

	store := newPruneAgentFakeStore(t)
	for i := range 6 {
		addPruneAgentFixture(&store.registry, "prj-beta", "win-beta-main", pruneAgentFixtureAgent{
			uid: fmt.Sprintf("agt-bulk-%d", i), name: fmt.Sprintf("bulk-%d", i), phase: coremetadata.PhaseOffline, at: pruneAgentStaleAt,
		})
	}
	store.registry = store.registry.Normalize()
	if err := store.registry.Validate(); err != nil {
		t.Fatal(err)
	}
	cmd, _ := newTestPruneAgentCommand(store.store(), fixedPruneAgentClock)
	stdout, _, err := runRoute(t, cmd, "--older-than", "720h", "--no-pane")
	if err != nil {
		t.Fatal(err)
	}
	if rows := len(pruneAgentRowUID.FindAllString(stdout, -1)); rows != selector.MaxCandidates {
		t.Fatalf("listed %d rows, want the bound of %d:\n%s", rows, selector.MaxCandidates, stdout)
	}
	// agt-beta-codex, agt-stale-failed, agt-kept, agt-has-ref, and six bulk rows.
	if !strings.Contains(stdout, "would delete 10 agents") || !strings.Contains(stdout, "... 5 more omitted") {
		t.Fatalf("the bounded listing hides the count:\n%s", stdout)
	}
}

// TestPruneAgentShortCircuitsAnEmptyRegistry writes nothing for an operator who
// has never created an Agent.
func TestPruneAgentShortCircuitsAnEmptyRegistry(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	store.registry = coremetadata.NewRegistry()
	cmd, probe := newTestPruneAgentCommand(store.store(), fixedPruneAgentClock)
	stdout, _, err := runRoute(t, cmd, "--older-than", "1h", "--no-pane", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "no Agents are registered") || store.transactions != 0 || probe.calls != 0 {
		t.Fatalf("stdout=%q transactions=%d calls=%d", stdout, store.transactions, probe.calls)
	}

	// A registry whose Agents match nothing opens no transaction under --yes.
	store = newPruneAgentFakeStore(t)
	cmd, _ = newTestPruneAgentCommand(store.store(), fixedPruneAgentClock)
	stdout, _, err = runRoute(t, cmd, "--older-than", "100000h", "--no-pane", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "no Agent matches --older-than 100000h0m0s --no-pane") || store.transactions != 0 {
		t.Fatalf("stdout=%q transactions=%d", stdout, store.transactions)
	}
}

// TestPruneDispatchesAgentToItsHandler keeps `prune agent` on the canonical
// dispatch table next to `prune project`.
func TestPruneDispatchesAgentToItsHandler(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("prune agent handler reached")
	probe := &retirementProbe{err: sentinel}
	cmd := &pruneCommand{agent: probe}
	args := []string{"agent", "--older-than", "1h", "--no-pane"}
	if err := cmd.Run(args, &strings.Builder{}, &strings.Builder{}); !errors.Is(err, sentinel) {
		t.Fatalf("prune agent dispatch error = %v", err)
	}
	if !reflect.DeepEqual(probe.calls, [][]string{args[1:]}) {
		t.Fatalf("prune agent handler calls = %v, want %v", probe.calls, [][]string{args[1:]})
	}
	if err := (&pruneCommand{}).Run([]string{"agent"}, &strings.Builder{}, &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("unconfigured prune agent error = %v", err)
	}
}
