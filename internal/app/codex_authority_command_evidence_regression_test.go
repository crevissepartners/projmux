package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

func TestInstalledCommandFailureCapturesOnceWithoutOutputOrFalseOutcome(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "calls")
	executable := filepath.Join(root, "fixture-command")
	script := "#!/bin/sh\n[ -z \"${TMUX+x}${TMUX_PANE+x}\" ] || exit 99\nprintf 'once\\n' >> \"$CP1_COMMAND_MARKER\"\nprintf '%s' 'PRIVATE-STDOUT'\nprintf '%s' \"$CP1_COMMAND_STDERR\" >&2\nexit \"$CP1_COMMAND_EXIT\"\n"
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CP1_COMMAND_MARKER", marker)
	t.Setenv("CP1_COMMAND_STDERR", "native Codex control refused the exact request (turn-start-failed)\n")
	t.Setenv("CP1_COMMAND_EXIT", "7")
	t.Setenv("TMUX", "PRIVATE-INHERITED")
	t.Setenv("TMUX_PANE", "%999")
	output, failure, err := executeInstalledRecoveryCommand(context.Background(), executable, "agent", "turn", "start", "uid:PRIVATE-SELECTOR", "--", "PRIVATE-PROMPT")
	if err == nil || output != nil || failure == nil || failure.ExitCode != 7 || failure.Stage != "control-response" || failure.Code != "turn-start-failed" || failure.Outcome != "unknown" {
		t.Fatalf("failure metadata=%+v err=%v", failure, err)
	}
	if calls, err := os.ReadFile(marker); err != nil || string(calls) != "once\n" {
		t.Fatal("failed command was retried or did not execute")
	}
	for _, ledger := range []any{
		installedRecoveryLedger{Result: "FAIL", Submissions: 3, Rows: []installedRecoveryRow{{Turns: []installedRecoveryTurn{{Status: codexappserver.TurnStateCompleted}, {Status: codexappserver.TurnStateCompleted}}}}, CommandFailure: failure},
		installedConnectionLedger{Result: "FAIL", CommandFailure: failure},
		installedRecoveryAttempt{InputIndex: 2, Stage: "submitting-turn", CommandFailure: failure},
	} {
		raw, err := json.Marshal(ledger)
		if err != nil || strings.Contains(string(raw), "PRIVATE") || strings.Contains(string(raw), "fixture-command") || !strings.Contains(string(raw), `"outcome":"unknown"`) {
			t.Fatalf("content escaped or outcome changed: %s", raw)
		}
	}
	// Success preserves the original command output; it does not inherit the
	// previous failure or turn stderr into an acceptance receipt.
	t.Setenv("CP1_COMMAND_EXIT", "0")
	output, failure, err = executeInstalledRecoveryCommand(context.Background(), executable, "get", "windows")
	if err != nil || failure != nil || string(output) != "PRIVATE-STDOUT" {
		t.Fatal("successful command output changed")
	}
}

func TestInstalledCommandFailureProjectionRequiresBoundedKnownCLIShape(t *testing.T) {
	known := "native Codex control refused the exact request (turn-start-failed)"
	suffix := "; Open Codex: `projmux focus pane uid:pane-a --project uid:proj-a --window uid:win-a`"
	for _, test := range []struct {
		name, text, stage, code string
		overflow                bool
	}{
		{"response", known + suffix + "\n", "control-response", "turn-start-failed", false},
		{"route", "exact Agent native control unavailable: resolve logical tmux route: PRIVATE-PATH", "control-binding", "logical-route", false},
		{"transport", "exact Agent native control unavailable: write native Codex control request: PRIVATE-ERROR", "control-transport", "request-write", false},
		{"recording", "exact Agent native control unavailable: turn response did not preserve the exact thread identity" + suffix, "turn-recording", "response-identity", false},
		{"usage", "agent turn start|steer requires <agent-ref> -- <text>; quote text as one argument", "cli-input", "invalid-turn-arguments", false},
		{"unknown", "PRIVATE-PAYLOAD (turn-start-failed)", "unclassified", "unclassified", false},
		{"wrong-code", "native Codex control refused the exact request (PRIVATE-CODE)", "unclassified", "unclassified", false},
		{"multiline", known + "\nPRIVATE-CONTENT", "unclassified", "unclassified", false},
		{"control-byte", known + "\x1b[31m", "unclassified", "unclassified", false},
		{"bad-recovery", known + "; Open Codex: `PRIVATE-ARGV`", "unclassified", "unclassified", false},
		{"extra-recovery", known + suffix + " PRIVATE-CONTENT", "unclassified", "unclassified", false},
		{"overflow", known, "unclassified", "unclassified", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			failure := projectInstalledCommandFailure([]string{"agent", "turn", "start", "PRIVATE-ARG"}, nil, errors.New("PRIVATE-ERROR"), installedCommandCapture{data: []byte(test.text), overflow: test.overflow})
			raw, err := json.Marshal(failure)
			if err != nil || failure.Stage != test.stage || failure.Code != test.code || failure.Outcome != "unknown" || strings.Contains(string(raw), "PRIVATE") || strings.Contains(string(raw), "pane-a") {
				t.Fatalf("unsafe projection: %s", raw)
			}
		})
	}
	var capture installedCommandCapture
	raw := []byte(strings.Repeat("PRIVATE", 1024))
	if n, err := capture.Write(raw); err != nil || n != len(raw) || !capture.overflow || len(capture.data) != installedCommandStderrLimit {
		t.Fatal("stderr was not bounded/drained")
	}
	if n, err := capture.Write(raw); err != nil || n != len(raw) || len(capture.data) != installedCommandStderrLimit {
		t.Fatal("stderr limit grew on later writes")
	}
}

func TestInstalledCommandFailureRetainsUnknownOutcomeAfterReadinessAndControl(t *testing.T) {
	for _, failureAt := range []string{"fresh-read", "wire", "recording", "success"} {
		t.Run(failureAt, func(t *testing.T) {
			command, store, runner, _ := installedRecoveryControlFixture(t)
			wire := &fakeExactControlWire{snapshot: codexappserver.LifecycleSnapshot{ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateIdle}}
			epoch := newCodexControlEpoch(wire, phase6CLIIdentity(), runner.epoch, wire.snapshot, func(codexLifecycleIdentity) bool { return true })
			command.controlCall = func(ctx context.Context, _ string, _ coremetadata.CodexEndpointRef, _ codexLifecycleIdentity, request agentControlRequest) (agentControlResponse, error) {
				return epoch.Handle(ctx, request), nil
			}
			out := recoveryRegistryObservation(store.registry, phase6CLIIdentity().AgentUID, phase6CLIEndpoint().EndpointGenerationID, nil)
			if observed := readInstalledRecoveryControl(command, out, true); observed.Stage != "ready" || wire.writes() != 0 {
				t.Fatal("readiness failed or submitted a turn")
			}
			switch failureAt {
			case "fresh-read":
				wire.snapshotErr = errors.New("PRIVATE-READ-ERROR")
			case "wire":
				wire.err = errors.New("PRIVATE-WIRE-ERROR")
			case "recording":
				command.store.update = func(func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
					return coremetadata.Registry{}, errors.New("PRIVATE-POST-ACCEPTANCE-STORE-ERROR")
				}
			}
			// Use the actual Cobra bridge and public agent handler, with only local
			// Registry/tmux/provider dependencies replaced by deterministic fixtures.
			handlers := map[string]cli.Handler{}
			for _, route := range cli.Routes() {
				handlers[route.Name] = func([]string, io.Writer, io.Writer) error { t.Fatal("unexpected CLI route"); return nil }
			}
			handlers["agent"] = command.Run
			var stdout, stderr bytes.Buffer
			root, err := cli.NewRoot(cli.RootOptions{Stdout: &stdout, Stderr: &stderr, Handlers: handlers})
			if err != nil {
				t.Fatal(err)
			}
			err = root.Execute([]string{"agent", "turn", "start", "uid:" + phase6CLIIdentity().AgentUID, "--", "PRIVATE-SYNTHETIC-INPUT"})
			if failureAt == "success" {
				if err != nil || wire.start != 1 {
					t.Fatal("public CLI start did not reach one exact write")
				}
				return
			}
			if err == nil || IsUsageError(err) || stdout.Len() != 0 {
				t.Fatal("expected runtime failure became a successful receipt or parse failure")
			}
			failure := projectInstalledCommandFailure([]string{"agent", "turn", "start"}, nil, err, installedCommandCapture{data: []byte(err.Error() + "\n")})
			wantCode := "unclassified"
			wantWrites := 1
			if failureAt == "fresh-read" {
				wantCode = "turn-state-unavailable"
				wantWrites = 0
			}
			if failureAt == "wire" {
				wantCode = "turn-start-failed"
			}
			if wire.start != wantWrites || failure.Code != wantCode || failure.Outcome != "unknown" {
				t.Fatalf("lost failure boundary: writes=%d metadata=%+v", wire.start, failure)
			}
		})
	}
}
