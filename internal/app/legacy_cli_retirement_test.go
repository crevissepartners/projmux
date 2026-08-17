package app

import (
	"bytes"
	"errors"
	"io"
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

func TestRemovedPublicArgvProcessMatrixIsUsageOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		args        []string
		replacement string
	}{
		{[]string{"attach", "auto"}, "runtime attach"},
		{[]string{"current"}, "get pane --current -o cwd"},
		{[]string{"focus", "--target", "alpha"}, "focus project|window|pane"},
		{[]string{"focus", "--uri", "projmux://focus"}, "focus project|window|pane"},
		{[]string{"kill", "tagged"}, "runtime stop"},
		{[]string{"sessions"}, "runtime sessions"},
		{[]string{"upgrade"}, "update apply"},
		{[]string{"usage"}, "agent usage"},
		{[]string{"notify", "push"}, "create notification"},
		{[]string{"notify", "list"}, "get notifications"},
		{[]string{"notify", "ack"}, "notification ack"},
		{[]string{"notify", "reconcile"}, "notification reconcile"},
		{[]string{"pin", "list"}, "pin project"},
		{[]string{"pin", "add"}, "pin project"},
		{[]string{"pin", "remove"}, "pin project"},
		{[]string{"pin", "toggle"}, "pin project"},
		{[]string{"pin", "clear"}, "pin project"},
		{[]string{"prune", "ephemeral"}, "runtime prune"},
		{[]string{"prune", "session-state"}, "prune snapshot"},
		{[]string{"session-state", "status"}, "get snapshots"},
		{[]string{"session-state", "save"}, "create snapshot"},
		{[]string{"session-state", "delete"}, "delete snapshot"},
		{[]string{"session-state", "restore"}, "restore snapshot"},
		{[]string{"session-state", "preview"}, "restore snapshot"},
		{[]string{"session-state", "popup"}, "restore snapshot"},
		{[]string{"tag", "list"}, "runtime tag"},
		{[]string{"tag", "toggle"}, "runtime tag"},
		{[]string{"tag", "clear"}, "runtime tag"},
		{[]string{"tag", "project"}, "runtime tag"},
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

func TestRemovedOldInternalArgvUsesUnknownCommandContract(t *testing.T) {
	t.Parallel()

	for _, token := range []string{"ai", "key-broker", "popup-wait-key", "preview", "session-popup", "status", "statusbar", "tmux"} {
		var stdout, stderr bytes.Buffer
		err := (&App{}).Run([]string{token}, &stdout, &stderr)
		if err == nil || IsUsageError(err) || !strings.Contains(err.Error(), "unknown command: "+token) {
			t.Fatalf("Run(%q) error=%v, want exit-1 unknown command", token, err)
		}
		if stdout.Len() != 0 || !strings.Contains(stderr.String(), "Commands:") {
			t.Fatalf("Run(%q) streams=(%q,%q), want stdout empty and root help on stderr", token, stdout.String(), stderr.String())
		}
		if shouldRunLegacyHookMigrations([]string{token}) {
			t.Fatalf("Run(%q) would perform a pre-dispatch migration", token)
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
		{"prune ephemeral", legacyRouteGate{name: "prune", target: &retirementProbe{}, allowedFirst: []string{"project", "snapshot"}, replacement: pruneReplacement}, []string{"ephemeral"}, "runtime prune"},
		{"current", retiredRoute{name: "current", replacement: func([]string) string { return "`projmux get pane --current -o cwd`" }}, nil, "get pane --current -o cwd"},
		{"kill tagged", retiredRoute{name: "kill", replacement: func([]string) string { return "`projmux runtime stop ...`" }}, []string{"tagged"}, "runtime stop"},
		{"notify ack", retiredRoute{name: "notify", replacement: notifyReplacement}, []string{"ack", "id"}, "notification ack"},
		{"sessions", retiredRoute{name: "sessions", replacement: func([]string) string { return "`projmux runtime sessions ...`" }}, nil, "runtime sessions"},
		{"session state save", retiredRoute{name: "session-state", replacement: sessionStateReplacement}, []string{"save"}, "create snapshot"},
		{"tag project", retiredRoute{name: "tag", replacement: func([]string) string { return "`projmux runtime tag ...`" }}, []string{"project", "list"}, "runtime tag"},
		{"upgrade", retiredRoute{name: "upgrade", replacement: func([]string) string { return "`projmux update apply ...`" }}, nil, "update apply"},
		{"usage", retiredRoute{name: "usage", replacement: func([]string) string { return "`projmux agent usage ...`" }}, nil, "agent usage"},
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
		{"prune project", []string{"project", "snapshot"}, []string{"project", "--missing"}},
		{"prune snapshot", []string{"project", "snapshot"}, []string{"snapshot", "--older-than", "24h"}},
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

func TestOldInternalTopLevelHandlersAreNotWired(t *testing.T) {
	t.Parallel()

	handlers := New().routeHandlers()
	for _, token := range []string{"tmux", "status", "statusbar", "preview", "session-popup", "key-broker", "popup-wait-key"} {
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
