package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

func parseInstalledRecoveryTurnReceipt(receipt, expectedThread, label string) (string, error) {
	refused := errors.New("installed turn receipt is malformed or does not match exact thread")
	if len(receipt) > 1024 || label == "" || !validInstalledReceiptIdentity(expectedThread) {
		return "", refused
	}
	// The public handler emits one line with QuoteToGraphic identities. Remove
	// only its one trailing LF; whitespace/quote trimming could hide ambiguity.
	fields, ok := strings.CutPrefix(strings.TrimSuffix(receipt, "\n"), label+" thread=")
	parts := strings.Split(fields, " ")
	if !ok || len(parts) != 2 {
		return "", refused
	}
	quotedTurn, ok := strings.CutPrefix(parts[1], "turn=")
	thread, threadOK := decodeInstalledReceiptIdentity(parts[0])
	turn, turnOK := decodeInstalledReceiptIdentity(quotedTurn)
	if !ok || !threadOK || !turnOK || thread != expectedThread {
		return "", refused
	}
	return turn, nil
}

func decodeInstalledReceiptIdentity(quoted string) (string, bool) {
	if len(quoted) < 2 || quoted[0] != '"' || quoted[len(quoted)-1] != '"' {
		return "", false
	}
	value, err := strconv.Unquote(quoted)
	return value, err == nil && validInstalledReceiptIdentity(value)
}

func validInstalledReceiptIdentity(value string) bool {
	if len(value) == 0 || len(value) > 256 {
		return false
	}
	// The installed conformance accepts bounded opaque ASCII identifiers (the
	// real fixtures use UUIDs), never presentation suffixes, paths or content.
	for index, char := range []byte(value) {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') {
			continue
		}
		if index == 0 || (char != '-' && char != '_') {
			return false
		}
	}
	return true
}

func TestInstalledRecoveryReceiptMatchesPublicControlTurnIdentity(t *testing.T) {
	command, _, _, requests := installedRecoveryControlFixture(t)
	receipt, stderr, err := runRoute(t, command, "turn", "start", "uid:"+phase6CLIIdentity().AgentUID, "--", "synthetic receipt input")
	if err != nil || stderr != "" || len(*requests) != 1 || (*requests)[0].Operation != agentControlOpStart {
		t.Fatal("public turn handler did not produce one exact control receipt")
	}
	completed := codexappserver.LifecycleSnapshot{ThreadID: phase6CLIIdentity().ThreadID, TurnID: "fixture-turn", TurnState: codexappserver.TurnStateCompleted}
	expected, err := parseInstalledRecoveryTurnReceipt(receipt, completed.ThreadID, command.agentActionText(agentActionSendTurn))
	if err != nil || expected != completed.TurnID {
		t.Fatalf("public receipt identity does not match completion consumer: parsed=%q snapshot=%q err=%v", expected, completed.TurnID, err)
	}
	if len(*requests) != 1 {
		t.Fatal("receipt parsing retried an input")
	}
	attempt := installedRecoveryAttempt{InputIndex: 2, Stage: "waiting-turn", ExpectedTurn: expected}
	raw, err := json.Marshal(attempt)
	if err != nil || !strings.Contains(string(raw), `"expectedTurn":"fixture-turn"`) || strings.Contains(string(raw), "synthetic") || strings.Contains(string(raw), "thread=") || strings.Contains(string(raw), command.agentActionText(agentActionSendTurn)) {
		t.Fatal("attempt did not retain only the validated expected turn identity")
	}
	// The installed wrapper must preserve the receipt bytes for this parser;
	// generic output trimming would silently accept malformed extra lines.
	executable := filepath.Join(t.TempDir(), "receipt-fixture")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf '%s' \"$CP1_RECEIPT_OUTPUT\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CP1_RECEIPT_OUTPUT", receipt+"\n")
	run := installedRecoveryCommand(t, t.Context(), nil)
	actual := run(executable, "agent", "turn", "start")
	if actual != receipt+"\n" {
		t.Fatal("installed wrapper changed receipt bytes")
	}
	if turn, err := parseInstalledRecoveryTurnReceipt(actual, completed.ThreadID, command.agentActionText(agentActionSendTurn)); err == nil || turn != "" {
		t.Fatal("extra output became a valid receipt")
	}
}

func TestInstalledRecoveryReceiptRejectsAmbiguousOrMalformedIdentity(t *testing.T) {
	const label = "Send new turn"
	const good = `Send new turn thread="thread-1" turn="turn-1"`
	for _, receipt := range []string{good, good + "\n", `Send new turn thread="\u0074hread-1" turn="\u0074urn-1"`} {
		turn, err := parseInstalledRecoveryTurnReceipt(receipt, "thread-1", label)
		if err != nil || turn != "turn-1" {
			t.Fatal("valid quoted receipt did not decode to exact identity")
		}
	}
	for _, test := range []struct{ name, receipt, thread string }{
		{"wrong-thread", good, "thread-2"},
		{"missing-expected-thread", good, ""},
		{"empty-turn", `Send new turn thread="thread-1" turn=""`, "thread-1"},
		{"missing-thread", `Send new turn turn="turn-1"`, "thread-1"},
		{"missing-turn", `Send new turn thread="thread-1"`, "thread-1"},
		{"duplicate-turn", good + ` turn="turn-2"`, "thread-1"},
		{"duplicate-thread", good + ` thread="thread-2"`, "thread-1"},
		{"unknown-field", good + ` unknown="PRIVATE"`, "thread-1"},
		{"wrong-action", `Steer current turn thread="thread-1" turn="turn-1"`, "thread-1"},
		{"unquoted", `Send new turn thread="thread-1" turn=turn-1`, "thread-1"},
		{"single-quoted", `Send new turn thread="thread-1" turn='a'`, "thread-1"},
		{"raw-quoted", "Send new turn thread=\"thread-1\" turn=`turn-1`", "thread-1"},
		{"unterminated", `Send new turn thread="thread-1" turn="turn-1`, "thread-1"},
		{"malformed-escape", `Send new turn thread="thread-1" turn="turn-\q"`, "thread-1"},
		{"escaped-control", `Send new turn thread="thread-1" turn="turn-\n1"`, "thread-1"},
		{"space-in-id", `Send new turn thread="thread-1" turn="turn 1"`, "thread-1"},
		{"path-id", `Send new turn thread="thread-1" turn="../PRIVATE"`, "thread-1"},
		{"truncated", good + "…[truncated]", "thread-1"},
		{"long-id", `Send new turn thread="thread-1" turn="` + strings.Repeat("a", 257) + `"`, "thread-1"},
		{"multiline", good + "\nPRIVATE", "thread-1"},
		{"extra-newline", good + "\n\n", "thread-1"},
		{"extra-space", good + " ", "thread-1"},
		{"oversized", good + strings.Repeat("PRIVATE", 200), "thread-1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			turn, err := parseInstalledRecoveryTurnReceipt(test.receipt, test.thread, label)
			if turn != "" || err == nil || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("malformed receipt acquired identity or exposed content")
			}
		})
	}
}
