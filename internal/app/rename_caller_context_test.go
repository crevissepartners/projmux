package app

import (
	"reflect"
	"strings"
	"testing"
)

func TestRenameWithoutActiveTargetRequiresExplicitSelector(t *testing.T) {
	t.Parallel()
	if newRenameCommand().activeTarget == nil {
		t.Fatal("CLI active target lookup missing")
	}
	for _, kind := range []string{"project", "window", "pane", "agent"} {
		t.Run(kind, func(t *testing.T) {
			store := newFakeResourceStore(t)
			before := store.registry.Clone()
			active := insideTmux("pan-alpha-zsh", "win-alpha-main")
			cmd := newTestRenameCommandWithActiveTarget(store, active)
			cmd.withoutActiveTarget()
			stdout, _, err := runRoute(t, cmd, kind, "--name", "renamed")
			if err == nil || !IsUsageError(err) || stdout != "" {
				t.Fatalf("empty selector = stdout %q err %v", stdout, err)
			}
			if active.calls != 0 || store.writes != 0 || !reflect.DeepEqual(store.registry, before) {
				t.Fatalf("refusal consulted ambient target or wrote: calls=%d writes=%d", active.calls, store.writes)
			}
		})
	}
}

func TestRenameWithoutActiveTargetUsesExactUIDAcrossManagedRoots(t *testing.T) {
	t.Parallel()
	for _, row := range []struct{ kind, uid string }{
		{"window", "win-home"}, {"pane", "pan-home-shell"}, {"agent", "agt-home"},
	} {
		t.Run(row.kind, func(t *testing.T) {
			store := newFakeResourceStore(t)
			addControlReadRoot(t, store)
			active := insideTmux("pan-alpha-zsh", "win-alpha-main")
			cmd := newTestRenameCommandWithActiveTarget(store, active)
			cmd.withoutActiveTarget()
			stdout, _, err := runRoute(t, cmd, row.kind, "uid:"+row.uid, "--name", "renamed")
			if err != nil || !strings.Contains(stdout, "receipt operation=rename."+row.kind) {
				t.Fatalf("exact UID = stdout %q err %v", stdout, err)
			}
			_, meta, ok := resourceFor(store.registry, resourceKindTokens[row.kind], row.uid)
			if !ok || meta.Name != "renamed" || active.calls != 0 || store.writes != 1 {
				t.Fatalf("rename = meta %+v calls %d writes %d", meta, active.calls, store.writes)
			}
		})
	}
}

func TestRenameWithoutActiveTargetPreservesExplicitScopeRefusals(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"agent", "uid:missing", "--name", "renamed"},
		{"agent", "uid:agt-home", "--project", "uid:prj-alpha", "--name", "renamed"},
		{"agent", "codex", "--name", "renamed"},
	} {
		t.Run(args[1]+args[2], func(t *testing.T) {
			store := newFakeResourceStore(t)
			addControlReadRoot(t, store)
			before := store.registry.Clone()
			active := insideTmux("pan-alpha-zsh", "win-alpha-main")
			cmd := newTestRenameCommandWithActiveTarget(store, active)
			cmd.withoutActiveTarget()
			stdout, _, err := runRoute(t, cmd, args...)
			if err == nil || !IsUsageError(err) || stdout != "" {
				t.Fatalf("explicit refusal = stdout %q err %v", stdout, err)
			}
			if active.calls != 0 || store.writes != 0 || !reflect.DeepEqual(store.registry, before) {
				t.Fatalf("refusal consulted ambient target or wrote: calls=%d writes=%d", active.calls, store.writes)
			}
		})
	}
}
