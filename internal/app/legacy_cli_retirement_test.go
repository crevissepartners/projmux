package app

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type retirementProbe struct {
	calls  [][]string
	stdout string
	stderr string
	err    error
}

func (p *retirementProbe) Run(args []string, stdout, stderr io.Writer) error {
	p.calls = append(p.calls, append([]string(nil), args...))
	_, _ = io.WriteString(stdout, p.stdout)
	_, _ = io.WriteString(stderr, p.stderr)
	return p.err
}

func TestRemovedMixedRootArgvProcessMatrixIsUsageOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		args        []string
		replacement string
	}{
		{[]string{"attach", "auto"}, "runtime attach"},
		{[]string{"focus", "--target", "alpha"}, "focus project|window|pane"},
		{[]string{"focus", "--uri", "projmux://focus"}, "focus project|window|pane"},
		{[]string{"pin", "list"}, "pin project"},
		{[]string{"pin", "add"}, "pin project"},
		{[]string{"pin", "remove"}, "pin project"},
		{[]string{"pin", "toggle"}, "pin project"},
		{[]string{"pin", "clear"}, "pin project"},
		{[]string{"prune", "ephemeral"}, "runtime prune"},
		{[]string{"prune", "session-state"}, "prune project"},
		{[]string{"prune", "snapshot"}, "prune project"},
	}
	for _, test := range tests {
		t.Run(strings.Join(test.args, "_"), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := (&App{}).Run(test.args, &stdout, &stderr)
			if err == nil || !IsUsageError(err) || !strings.Contains(err.Error(), test.replacement) {
				t.Fatalf("Run(%q) error=%v, want replacement %q usage error", test.args, err, test.replacement)
			}
			if stdout.Len() != 0 || stderr.Len() != 0 {
				t.Fatalf("Run(%q) streams=(%q,%q), want empty", test.args, stdout.String(), stderr.String())
			}
			if shouldRunLegacyHookMigrations(test.args) {
				t.Fatalf("Run(%q) would perform a pre-dispatch migration", test.args)
			}
		})
	}
}

func TestRemovedTopLevelArgvUsesUnknownCommandContract(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"ai", "ingest", "codex-hook"},
		{"current", "--help"},
		{"kill", "tagged"},
		{"notify", "list"},
		{"sessions", "--ui=popup"},
		{"session-state", "save"},
		{"tag", "project", "list"},
		{"upgrade", "--dry-run"},
		{"usage", "--json"},
		{"key-broker"},
		{"popup-wait-key"},
		{"preview", "select"},
		{"session-popup", "open"},
		{"status", "resources"},
		{"statusbar", "click"},
		{"tmux", "print-config"},
	} {
		token := args[0]
		var stdout, stderr bytes.Buffer
		err := (&App{}).Run(args, &stdout, &stderr)
		if err == nil || IsUsageError(err) || !strings.Contains(err.Error(), "unknown command: "+token) {
			t.Fatalf("Run(%q) error=%v, want exit-1 unknown command", args, err)
		}
		if stdout.Len() != 0 || !strings.Contains(stderr.String(), "Commands:") {
			t.Fatalf("Run(%q) streams=(%q,%q), want stdout empty and root help on stderr", args, stdout.String(), stderr.String())
		}
		if shouldRunLegacyHookMigrations(args) {
			t.Fatalf("Run(%q) would perform a pre-dispatch migration", args)
		}
	}
}

func TestRemovedAIRootRejectsEveryArgvBeforeMigration(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"ai"},
		{"ai", "--help"},
		{"ai", "split"},
		{"ai", "notify", "reset", "%7"},
		{"ai", "ingest", "codex-hook"},
		{"ai", "ingest", "claude-hook"},
		{"ai", "ingest", "antigravity-hook", "--event", "Stop"},
		{"ai", "ingest", "bell", "--pane", "%7"},
	} {
		var stdout, stderr bytes.Buffer
		err := (&App{}).Run(args, &stdout, &stderr)
		if err == nil || IsUsageError(err) || err.Error() != "unknown command: ai" {
			t.Fatalf("Run(%q) error=%v, want root unknown-command error", args, err)
		}
		if stdout.Len() != 0 || !strings.Contains(stderr.String(), "Commands:") {
			t.Fatalf("Run(%q) streams=(%q,%q), want stdout empty and root help on stderr", args, stdout.String(), stderr.String())
		}
		if shouldRunLegacyHookMigrations(args) {
			t.Fatalf("Run(%q) would mutate before root rejection", args)
		}
	}
}

func TestLegacyUpdaterHandoffAcceptsOnlyExactTmuxApply(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PROJMUX_CWD", "")

	sentinel := errors.New("candidate apply reached")
	probe := &retirementProbe{stdout: "apply stdout\n", stderr: "apply stderr\n", err: sentinel}
	app := &App{config: &configCommand{tmux: probe}}
	var stdout, stderr bytes.Buffer
	err := app.Run([]string{"tmux", "apply"}, &stdout, &stderr)
	if err != sentinel {
		t.Fatalf("exact updater handoff error = %v, want handler error %v", err, sentinel)
	}
	if !reflect.DeepEqual(probe.calls, [][]string{{"apply"}}) {
		t.Fatalf("exact updater handoff calls = %#v, want canonical apply once", probe.calls)
	}
	if stdout.String() != probe.stdout || stderr.String() != probe.stderr {
		t.Fatalf("exact updater handoff streams = (%q,%q), want unchanged (%q,%q)",
			stdout.String(), stderr.String(), probe.stdout, probe.stderr)
	}
	if got := normalizeLegacyUpdaterHandoff([]string{"tmux", "apply"}); !reflect.DeepEqual(got, []string{"config", "apply"}) {
		t.Fatalf("normalized argv = %#v, want [config apply]", got)
	}
	if !shouldRunLegacyHookMigrations(normalizeLegacyUpdaterHandoff([]string{"tmux", "apply"})) {
		t.Fatal("exact updater handoff skipped canonical pre-dispatch migration ordering")
	}

	for _, args := range [][]string{
		{"tmux"},
		{"tmux", "apply", "--no-reload"},
		{"tmux", "apply", "extra"},
		{"tmux", "print-config"},
		{"tmux", "--help"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			probe.calls = nil
			stdout.Reset()
			stderr.Reset()
			err := app.Run(args, &stdout, &stderr)
			if err == nil || IsUsageError(err) || !strings.Contains(err.Error(), "unknown command: tmux") {
				t.Fatalf("Run(%q) error=%v, want root unknown-command error", args, err)
			}
			if len(probe.calls) != 0 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "Commands:") {
				t.Fatalf("Run(%q) calls=%v streams=(%q,%q), want no handler/stdout and root help on stderr",
					args, probe.calls, stdout.String(), stderr.String())
			}
			if got := normalizeLegacyUpdaterHandoff(args); !reflect.DeepEqual(got, args) {
				t.Fatalf("Run(%q) normalized to %#v, want unchanged", args, got)
			}
		})
	}
}

func TestRemovedPublicArgvMatrixReturnsReplacementUsageWithoutHandlerReach(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		command     rawArgvCommand
		args        []string
		replacement string
	}{
		{"attach auto", legacyRouteGate{name: "attach", target: &retirementProbe{}, allowedFirst: []string{"project"}, replacement: func([]string) string { return "`projmux runtime attach ...`" }}, []string{"auto"}, "runtime attach"},
		{"focus target", legacyRouteGate{name: "focus", target: &retirementProbe{}, allowedFirst: focusKinds, replacement: func([]string) string { return "`projmux focus project|window|pane ...`" }}, []string{"--target", "alpha"}, "focus project|window|pane"},
		{"pin direct", legacyRouteGate{name: "pin", target: &retirementProbe{}, allowedFirst: []string{"project"}, replacement: func([]string) string { return "`projmux pin project ...`" }}, []string{"toggle", "/repo"}, "pin project"},
		{"prune ephemeral", legacyRouteGate{name: "prune", target: &retirementProbe{}, allowedFirst: []string{"agent", "project"}, replacement: pruneReplacement}, []string{"ephemeral"}, "runtime prune"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			err := test.command.Run(test.args, &stdout, &stderr)
			if err == nil || !IsUsageError(err) || !strings.Contains(err.Error(), test.replacement) {
				t.Fatalf("error=%v, want replacement %q UsageError", err, test.replacement)
			}
			if stdout.Len() != 0 || stderr.Len() != 0 {
				t.Fatalf("streams=(%q,%q), want empty", stdout.String(), stderr.String())
			}
		})
	}
}

func TestMixedLegacyRootsForwardOnlySurvivingCanonicalChildren(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		allowed []string
		args    []string
	}{
		{"attach project", []string{"project"}, []string{"project", "alpha"}},
		{"focus project", focusKinds, []string{"project", "alpha"}},
		{"focus window", focusKinds, []string{"window", "win", "--project", "alpha"}},
		{"focus pane", focusKinds, []string{"pane", "pan", "--project", "alpha", "--window", "win"}},
		{"pin project", []string{"project"}, []string{"project", "list"}},
		{"prune agent", []string{"agent", "project"}, []string{"agent", "--older-than", "720h", "--no-pane"}},
		{"prune project", []string{"agent", "project"}, []string{"project", "--missing"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			sentinel := errors.New("canonical handler reached")
			probe := &retirementProbe{err: sentinel}
			gate := legacyRouteGate{name: strings.Fields(test.name)[0], target: probe, allowedFirst: test.allowed, replacement: func([]string) string { return "replacement" }}
			if err := gate.Run(test.args, io.Discard, io.Discard); err != sentinel || !reflect.DeepEqual(probe.calls, [][]string{test.args}) {
				t.Fatalf("error=%v calls=%v, want exact canonical forwarding", err, probe.calls)
			}
		})
	}
}

// TestRemovedSnapshotRoutesAreUnknownCommands pins the removal of the Project
// snapshot surface on the real application graph. Every former spelling fails
// before any handler work: `restore` and the hidden `session-state` root are
// unknown commands, and the three kind spellings plus `prune snapshot` are
// refused by the verb that no longer owns the kind. Nothing is written under
// the isolated HOME, in particular no legacy sessions directory.
func TestRemovedSnapshotRoutesAreUnknownCommands(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("PROJMUX_CWD", "")
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")

	for _, test := range []struct {
		args  []string
		usage bool
		want  string
	}{
		{args: []string{"create", "snapshot"}, usage: true, want: "create snapshot is not available"},
		{args: []string{"get", "snapshots"}, usage: true, want: "get snapshots is not available"},
		{args: []string{"get", "snapshot"}, usage: true, want: "get snapshot is not available"},
		{args: []string{"delete", "snapshot", "alpha"}, usage: true, want: "delete snapshot is not available"},
		{args: []string{"prune", "snapshot", "--older-than", "24h"}, usage: true, want: "`projmux prune snapshot --older-than 24h` was removed"},
		{args: []string{"restore", "snapshot", "--session", "alpha", "--dry-run"}, want: "unknown command: restore"},
		{args: []string{"session-state", "status"}, want: "unknown command: session-state"},
	} {
		t.Run(strings.Join(test.args, "_"), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := New().Run(test.args, &stdout, &stderr)
			if err == nil || IsUsageError(err) != test.usage || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Run(%q) error=%v (usage=%t), want %q (usage=%t)", test.args, err, IsUsageError(err), test.want, test.usage)
			}
			if stdout.Len() != 0 {
				t.Fatalf("Run(%q) stdout=%q, want empty", test.args, stdout.String())
			}
		})
	}
	if _, err := os.Stat(filepath.Join(root, "state", "projmux", "sessions")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed snapshot routes touched the legacy sessions directory: %v", err)
	}
}

func TestOldInternalTopLevelHandlersAreNotWired(t *testing.T) {
	t.Parallel()

	handlers := New().routeHandlers()
	for _, token := range []string{
		"ai", "current", "kill", "notify", "sessions", "session-state", "tag", "upgrade", "usage",
		"tmux", "status", "statusbar", "preview", "session-popup", "key-broker", "popup-wait-key",
	} {
		if _, ok := handlers[token]; ok {
			t.Errorf("old internal alias %q still has a top-level handler", token)
		}
	}
	for _, token := range []string{"internal", "attach", "focus", "pin", "prune"} {
		if _, ok := handlers[token]; !ok {
			t.Errorf("surviving route %q has no handler", token)
		}
	}
}
