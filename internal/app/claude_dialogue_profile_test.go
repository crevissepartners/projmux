package app

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func TestClaudeDialogueCreateOptInRefusesUnsupportedOrUnconfiguredLaunchBeforeWrites(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"agent", "--provider", "codex", "--dialogue-reply-only"},
		{"agent", "--provider", "claude", "--dialogue-reply-only=wrong"},
		{"agent", "--provider", "claude", "--dialogue-reply-only", "--", "initial tool request"},
		{"agent", "--provider", "claude", "--dialogue-reply-only", "--project", "alpha", "--window", "review"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			store := newFakeResourceStore(t)
			mux := newFakeTmux()
			cmd, launcher := newTestAgentCreateCommand(t, store, mux)
			before := store.snapshot()
			if _, _, err := runRoute(t, cmd, args...); err == nil {
				t.Fatal("unsupported reply-only create succeeded")
			}
			if store.transactions != 0 || store.writes != 0 || len(launcher.plans) != 0 || len(splitWindowCalls(mux)) != 0 || before != store.snapshot() {
				t.Fatal("refused reply-only create changed Registry or provider runtime")
			}
		})
	}
}

func TestClaudeDialogueResumeOptInKeepsRunningAndNonClaudeRefusalsReadOnly(t *testing.T) {
	t.Parallel()
	for _, running := range []bool{false, true} {
		store := newFakeResourceStore(t)
		mux := newFakeTmux()
		cmd, launcher, _, _ := newTestAgentResumeCommand(t, store, mux)
		agent, _ := store.registry.Agent("agt-beta-codex")
		if running {
			agent.Status.Phase = coremetadata.PhaseRunning
		}
		before := store.snapshot()
		if _, _, err := runRoute(t, cmd, "resume", "uid:agt-beta-codex", "--dialogue-reply-only"); err == nil {
			t.Fatal("invalid reply-only resume succeeded")
		}
		if store.transactions != 0 || store.writes != 0 || len(launcher.plans) != 0 || len(splitWindowCalls(mux)) != 0 || before != store.snapshot() {
			t.Fatal("refused resume changed existing UID or runtime")
		}
	}
}

func TestClaudeDialogueOptInIsAnExplicitSupervisorEnvelopeNotAnInheritedDefault(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{false, true} {
		store := newFakeResourceStore(t)
		cmd, _ := newTestSuperviseCommand(t, store, processOutcome{}, nil)
		observed := !enabled
		cmd.runActivation = func(_ []string, _ string, spec superviseSpec) (processOutcome, error) {
			observed = spec.DialogueReplyOnly
			return processOutcome{}, nil
		}
		spec := superviseSpec{PaneUID: "pane-fixture", AgentUID: "agent-fixture", Generation: "generation-fixture", OperationID: "operation-fixture", DialogueReplyOnly: enabled}
		argv := superviseArgv("projmux", spec, "", []string{"provider"})
		_ = cmd.Run(argv[3:], io.Discard, io.Discard)
		if observed != enabled {
			t.Fatalf("supervisor mode=%t want %t", observed, enabled)
		}
		gate := activationExecArgv("projmux", spec, "", 3, []string{"provider"})
		if slices.Contains(gate, "--dialogue-reply-only") != enabled {
			t.Fatal("activation gate did not preserve explicit invocation mode")
		}
	}
}

func TestAgentMessageAndReplyOnlyPreflightDoNotMigrateGlobalOrProjectHooks(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config")
	project := filepath.Join(root, "project")
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("PROJMUX_CWD", project)
	scripts := []string{filepath.Join(config, "projmux/hooks/post-create"), filepath.Join(project, ".projmux/post-create")}
	original := []byte("#!/bin/sh\necho legacy-fixture\n")
	for _, path := range scripts {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, original, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, argv := range [][]string{
		{"internal", "agent-hook", "ingest", "claude-hook", "--unknown-before-auth"},
		{"internal", "claude-endpoint-register", "--unknown-before-auth"},
		{"internal", "claude-endpoint-helper", "--unknown-before-auth"},
		{"agent", "message", "send", "--unknown-before-auth"},
		{"agent", "message", "qualify", "--unknown-before-auth"},
		{"create", "agent", "--dialogue-reply-only=invalid"},
		{"create", "agent", "-dialogue-reply-only=invalid"},
		{"agent", "resume", "uid:missing", "-dialogue-reply-only=invalid"},
		{"agent", "resume", "uid:missing", "--dialogue-reply-only=invalid"},
	} {
		if shouldRunLegacyHookMigrations(argv) {
			t.Fatalf("unrelated migration admitted before auth: %v", argv)
		}
		if err := New().Run(argv, io.Discard, io.Discard); err == nil && argv[0] != "internal" {
			t.Fatal("malformed preflight succeeded")
		}
		for _, path := range scripts {
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, original) {
				t.Fatalf("legacy hook changed before auth: %v", err)
			}
		}
	}
	for _, path := range []string{filepath.Join(config, "projmux/config.toml"), filepath.Join(project, ".projmux.toml")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("preflight created migration output: %s", path)
		}
	}
	if !shouldRunLegacyHookMigrations([]string{"create", "agent", "--", "--dialogue-reply-only"}) {
		t.Fatal("payload was treated as activation opt-in")
	}
}

func TestClaudeDialogueCleanupFailureStillRecordsActualProviderOutcome(t *testing.T) {
	store := newFakeResourceStore(t)
	activatePaneFixture(t, store, "pan-alpha-log", "", "gen-exact")
	command, _ := newTestSuperviseCommand(t, store, processOutcome{ExitCode: 17}, errClaudeDialogueCleanup)
	err := command.Run([]string{"--pane-uid", "pan-alpha-log", "--generation", "gen-exact", "--dialogue-reply-only", "--", "owned-provider"}, io.Discard, io.Discard)
	if !errors.Is(err, errClaudeDialogueCleanup) {
		t.Fatal("cleanup failure was hidden", err)
	}
	receipts := readTestTerminationJournal(t, command)
	if len(receipts) != 1 || receipts[0].ExitCode == nil || *receipts[0].ExitCode != 17 {
		t.Fatal("actual outcome was replaced by launch failure")
	}
}
