package projmux

import (
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	runtime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

// relaunchHostFixture prepares the cascade fixture and adds one Agent per Pane
// shape the relaunch host rule distinguishes, all in window-01:
//
//	agent-rh-none:  no Pane
//	agent-rh-tmux:  one tmux Pane
//	agent-rh-proc1: one process Pane
//	agent-rh-proc2: two process Panes (ambiguous)
//	agent-rh-mixed: one tmux Pane and one process Pane
//
// It returns the prepared handle and the Registry the same file loads as.
func relaunchHostFixture(t *testing.T) (*PreparedRegistrySnapshot, metadata.Registry) {
	t.Helper()
	agt := func(uid string) metadata.OwnerRef { return metadata.OwnerRef{Kind: metadata.KindAgent, UID: uid} }
	return cascadeFixture(t, func(r *metadata.Registry) {
		r.Agents = append(r.Agents,
			cascadeAgent("agent-rh-none", "window-01", ""),
			cascadeAgent("agent-rh-tmux", "window-01", ""),
			cascadeAgent("agent-rh-proc1", "window-01", ""),
			cascadeAgent("agent-rh-proc2", "window-01", ""),
			cascadeAgent("agent-rh-mixed", "window-01", ""),
		)
		r.Panes = append(r.Panes,
			cascadePane("pane-rh-tmux", agt("agent-rh-tmux"), metadata.PaneRoleAgent, metadata.RuntimeTmux),
			cascadePane("pane-rh-proc1", agt("agent-rh-proc1"), metadata.PaneRoleAgent, metadata.RuntimeProcess),
			cascadePane("pane-rh-proc2a", agt("agent-rh-proc2"), metadata.PaneRoleAgent, metadata.RuntimeProcess),
			cascadePane("pane-rh-proc2b", agt("agent-rh-proc2"), metadata.PaneRoleAgent, metadata.RuntimeProcess),
			cascadePane("pane-rh-mixed-t", agt("agent-rh-mixed"), metadata.PaneRoleAgent, metadata.RuntimeTmux),
			cascadePane("pane-rh-mixed-p", agt("agent-rh-mixed"), metadata.PaneRoleAgent, metadata.RuntimeProcess),
		)
	})
}

// For an Agent with a row, RelaunchCurrentHost equals the internal
// runtime.RelaunchCurrentHost on the same Registry; the expected value is
// always that function's own result, never a literal.
func TestRelaunchCurrentHostEqualsInternal(t *testing.T) {
	prepared, reg := relaunchHostFixture(t)
	for _, tc := range []struct {
		name string
		uid  string
		want string // fixes the fixture shape, not the compared value
	}{
		{"no-pane", "agent-rh-none", "tmux"},
		{"tmux-pane-only", "agent-rh-tmux", "tmux"},
		{"one-process-pane", "agent-rh-proc1", "process"},
		{"two-process-panes-ambiguous", "agent-rh-proc2", "tmux"},
		{"tmux-and-one-process-pane", "agent-rh-mixed", "process"},
		{"fixture-process-agent", "agent-31", "process"},
		{"fixture-tmux-agent", "agent-21", "tmux"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := prepared.RelaunchCurrentHost(tc.uid)
			if want := runtime.RelaunchCurrentHost(reg, tc.uid); got != want {
				t.Fatalf("RelaunchCurrentHost(%q) = %q, internal = %q", tc.uid, got, want)
			}
			if got != tc.want {
				t.Fatalf("fixture shape changed: %q, want %q", got, tc.want)
			}
		})
	}
}

// A handle that was not prepared and an agentUID with no Agent row answer ""
// with no error. This is where the public answer differs from the internal
// rule, which has no Agent-row check and answers "tmux" for any uid that owns
// no single process Pane.
func TestRelaunchCurrentHostEmptyForUnpreparedHandleAndMissingAgentDiffersFromInternal(t *testing.T) {
	prepared, reg := relaunchHostFixture(t)
	for _, uid := range []string{"agent-does-not-exist", "", "window-01", "pane-31"} {
		if got := prepared.RelaunchCurrentHost(uid); got != "" {
			t.Fatalf("RelaunchCurrentHost(%q) = %q, want empty for a uid with no Agent row", uid, got)
		}
	}
	if got := runtime.RelaunchCurrentHost(reg, "agent-does-not-exist"); got != "tmux" {
		t.Fatalf("internal rule for a missing Agent = %q; the public empty answer is meant to differ from it", got)
	}
	var nilHandle *PreparedRegistrySnapshot
	if got := nilHandle.RelaunchCurrentHost("agent-rh-proc1"); got != "" {
		t.Fatalf("nil handle = %q, want empty", got)
	}
	if got := (&PreparedRegistrySnapshot{}).RelaunchCurrentHost("agent-rh-proc1"); got != "" {
		t.Fatalf("zero handle = %q, want empty", got)
	}
}

// A process Pane whose owner is a uid with no Agent row is an orphan. The
// internal rule does not check for the Agent row, so it answers "process" for
// that uid; the public method answers "" because there is no Agent row.
func TestRelaunchCurrentHostEmptyForMissingAgentWithOrphanProcessPane(t *testing.T) {
	prepared, reg := cascadeFixture(t, func(r *metadata.Registry) {
		r.Panes = append(r.Panes, cascadePane("pane-rh-orphan",
			metadata.OwnerRef{Kind: metadata.KindAgent, UID: "agent-rh-gone"},
			metadata.PaneRoleAgent, metadata.RuntimeProcess))
	})
	if _, ok := reg.Agent("agent-rh-gone"); ok {
		t.Fatal("fixture shape changed: agent-rh-gone has an Agent row")
	}
	if got := runtime.RelaunchCurrentHost(reg, "agent-rh-gone"); got != "process" {
		t.Fatalf("internal rule = %q, want process (it does not check for the Agent row)", got)
	}
	if got := prepared.RelaunchCurrentHost("agent-rh-gone"); got != "" {
		t.Fatalf("RelaunchCurrentHost(agent-rh-gone) = %q, want empty", got)
	}
}
