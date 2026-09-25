package diagnostics

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRecordOutcomeNamesCatalogRouteErrors pins that an error outcome of a
// current catalog route carries its allowlisted command and subcommand, that
// unknown argv stays unnamed, and that a successful non-mutating route still
// writes nothing.
func TestRecordOutcomeNamesCatalogRouteErrors(t *testing.T) {
	start := time.Now().Add(-time.Millisecond)
	errorCases := []struct {
		args           []string
		usage          bool
		wantCommand    string
		wantSubcommand string
	}{
		{args: []string{"get", "pane", "--pane", "uid:pane-nope"}, wantCommand: "get", wantSubcommand: "pane"},
		{args: []string{"get", "agents", "-p", "uid:proj-nope"}, wantCommand: "get", wantSubcommand: "agents"},
		{args: []string{"get", "agent", "-p", "uid:proj-nope"}, wantCommand: "get", wantSubcommand: "agents"},
		{args: []string{"delete", "agent", "uid:agent-nope", "--yes"}, wantCommand: "delete", wantSubcommand: "agent"},
		{args: []string{"delete", "agents", "uid:agent-nope", "--yes"}, wantCommand: "delete", wantSubcommand: "agent"},
		{args: []string{"config", "agent-questions", "--bogus"}, usage: true, wantCommand: "config", wantSubcommand: "agent-questions"},
		{args: []string{"create", "window"}, wantCommand: "create", wantSubcommand: "window"},
		{args: []string{"reconcile", "resources"}, wantCommand: "reconcile", wantSubcommand: "resources"},
		{args: []string{"agent", "message", "send", "x"}, wantCommand: "agent", wantSubcommand: "message"},
		{args: []string{"internal", "agent-pane", "picker", "--bogus"}, usage: true, wantCommand: "agent-pane", wantSubcommand: "picker"},
		{args: []string{"internal", "supervise"}, wantCommand: "supervise"},
		{args: []string{"help", "nosuchtopic"}, usage: true, wantCommand: "help"},
	}
	for _, tt := range errorCases {
		t.Run("error "+strings.Join(tt.args, " "), func(t *testing.T) {
			store := NewStore(filepath.Join(t.TempDir(), "logs", LogFileName))
			if err := RecordOutcome(store, tt.args, "route-error", "0.8.4", "tmux", start, errors.New("private failure"), tt.usage, false); err != nil {
				t.Fatal(err)
			}
			events, err := store.Read()
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 1 {
				t.Fatalf("error appended %d events, want 1: %#v", len(events), events)
			}
			if events[0].Result != "error" || events[0].Command != tt.wantCommand || events[0].Subcommand != tt.wantSubcommand {
				t.Fatalf("error event = %q/%q result %q, want %q/%q error", events[0].Command, events[0].Subcommand, events[0].Result, tt.wantCommand, tt.wantSubcommand)
			}
		})
	}

	for _, args := range [][]string{{"nosuchcmd", "secret-arg"}, {"internal", "nosuch", "secret-arg"}, {"supervise", "secret-arg"}} {
		t.Run("unknown "+strings.Join(args, " "), func(t *testing.T) {
			store := NewStore(filepath.Join(t.TempDir(), "logs", LogFileName))
			if err := RecordOutcome(store, args, "unknown-error", "0.8.4", "tmux", start, errors.New("unknown command"), true, false); err != nil {
				t.Fatal(err)
			}
			events, err := store.Read()
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 1 || events[0].Command != "" || events[0].Subcommand != "" {
				t.Fatalf("unknown argv events = %#v, want one unnamed error", events)
			}
			data, err := os.ReadFile(store.path)
			if err != nil {
				t.Fatal(err)
			}
			for _, arg := range args {
				if bytes.Contains(data, []byte(arg)) {
					t.Fatalf("journal leaked argv %q: %s", arg, data)
				}
			}
		})
	}

	for _, args := range [][]string{
		{"get", "projects"},
		{"create", "window"},
		{"config", "apply"},
		{"reconcile", "resources"},
		{"agent", "status"},
		{"delete", "agent", "uid:agent-x", "--yes"},
		{"label", "pane", "uid:pane-x", "k=v"},
		{"rename", "pane", "uid:pane-x", "new"},
		{"runtime", "diagnostics"},
		{"internal", "supervise"},
	} {
		t.Run("success "+strings.Join(args, " "), func(t *testing.T) {
			store := NewStore(filepath.Join(t.TempDir(), "logs", LogFileName))
			if err := RecordOutcome(store, args, "route-success", "0.8.4", "tmux", start, nil, false, false); err != nil {
				t.Fatal(err)
			}
			events, err := store.Read()
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 0 {
				t.Fatalf("newly classified success appended %d events, want 0: %#v", len(events), events)
			}
		})
	}
}
