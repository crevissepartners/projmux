package app

import (
	"os"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// The endpoint helper sends a plain check every 100ms and looks up its
// registration on its own Registry reloads. The owner answers both without
// reloading an unchanged Registry.
func TestClaudeProcessHelperCheckReloadsRegistryOnlyWhenItChanged(t *testing.T) {
	f := newProcessClaudeFixture(t, nil)
	proof := f.proof(t)
	// Registration (lookup, register, helper) keeps the full transaction. Once it
	// settles, the live helper's ticks alone leave an interval of five ticks
	// without one; the idle floor may land in an interval, so try a few.
	idle := false
	for deadline := time.Now().Add(10 * time.Second); !idle && time.Now().Before(deadline); {
		before := f.registryChecks.Load()
		time.Sleep(5 * claudeEndpointPollInterval)
		idle = f.registryChecks.Load() == before
	}
	if !idle {
		t.Fatal("the idle owner kept running Registry transactions on helper ticks")
	}
	// A Registry write racing the window legitimately forces one reload, so
	// retry a window whose Registry identity moved.
	for attempt := 0; ; attempt++ {
		identity, err := f.store.RegistryFileIdentity()
		if err != nil {
			t.Fatal(err)
		}
		before, started := f.registryChecks.Load(), time.Now()
		for range 20 {
			if !checkClaudeProcessHost(proof, false) {
				t.Fatal("current helper check refused")
			}
			if _, err := lookupClaudeProcessRegistration(proof); err != nil {
				t.Fatalf("current helper lookup refused: %v", err)
			}
		}
		loads := f.registryChecks.Load() - before
		// Only the idle floor may force a reload of an unchanged Registry. It runs
		// from the gate's last evaluation, which may predate this window.
		allowed := 1 + int64(time.Since(started)/claudeEndpointIdleRegistryFloor)
		after, err := f.store.RegistryFileIdentity()
		if err != nil {
			t.Fatal(err)
		}
		if after != identity && attempt < 3 {
			continue
		}
		if after != identity {
			t.Fatal("Registry kept changing during every check window")
		}
		if loads > allowed {
			t.Fatalf("20 checks and lookups against an unchanged Registry ran %d Registry transactions, want at most %d", loads, allowed)
		}
		return
	}
}

// Every Registry change is evaluated by the next helper check, so a moved
// generation, a deleted Agent, or a restored stale Registry refuses at once.
func TestClaudeProcessHelperCheckRefusesTheNextCheckAfterARegistryChange(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*testing.T, *processClaudeFixture)
	}{
		{"generation moved", func(t *testing.T, f *processClaudeFixture) {
			updateProcessClaudeRegistry(t, f, func(reg *coremetadata.Registry) error {
				pane, _ := reg.Pane(f.binding.Pane)
				pane.Status.ProcessSession.Binding.Generation += "-next"
				return nil
			})
		}},
		{"agent deleted", func(t *testing.T, f *processClaudeFixture) {
			updateProcessClaudeRegistry(t, f, func(reg *coremetadata.Registry) error {
				return intmetadata.DefaultMutator().DeleteAgent(reg, f.binding.Agent)
			})
		}},
		{"stale registry restored in place", func(t *testing.T, f *processClaudeFixture) {
			current, err := os.ReadFile(f.path)
			if err != nil {
				t.Fatal(err)
			}
			updateProcessClaudeRegistry(t, f, func(reg *coremetadata.Registry) error {
				pane, _ := reg.Pane(f.binding.Pane)
				pane.Status.ProcessSession.Binding.Generation += "-backup"
				return nil
			})
			stale, err := os.ReadFile(f.path)
			if err != nil {
				t.Fatal(err)
			}
			restoreProcessClaudeRegistry(t, f, current)
			if !checkClaudeProcessHost(f.proof(t), false) {
				t.Fatal("restoring the current Registry refused the helper check")
			}
			restoreProcessClaudeRegistry(t, f, stale)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProcessClaudeFixture(t, nil)
			proof := f.proof(t)
			for range 5 {
				if !checkClaudeProcessHost(proof, false) {
					t.Fatal("current helper check refused")
				}
			}
			tc.change(t, f)
			if checkClaudeProcessHost(proof, false) {
				t.Fatal("the first helper check after the Registry change was admitted")
			}
			if checkClaudeProcessHost(proof, false) {
				t.Fatal("a refused Registry evaluation was cached as current")
			}
		})
	}
}

func updateProcessClaudeRegistry(t *testing.T, f *processClaudeFixture, mutate func(*coremetadata.Registry) error) {
	t.Helper()
	if _, err := f.store.Update(mutate); err != nil {
		t.Fatal(err)
	}
}

// restoreProcessClaudeRegistry copies a saved Registry over the live file in
// place, as a manual backup restore does, keeping the file's inode.
func restoreProcessClaudeRegistry(t *testing.T, f *processClaudeFixture, raw []byte) {
	t.Helper()
	before, err := os.Stat(f.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(f.path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("in-place restore replaced the Registry inode")
	}
}
