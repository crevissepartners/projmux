package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/diagnostics"
)

func diagnosticsFixture(id, level, component string) diagnostics.Event {
	return diagnostics.Event{At: "2026-08-12T01:02:03Z", Level: level, Component: component, Event: "command.outcome", Result: map[bool]string{true: "error", false: "success"}[level == "error"], DurationMS: 7, RunID: id, Version: "0.8.4", MuxBackend: "tmux", Command: "notify", Subcommand: "push", Kind: map[bool]string{true: "runtime", false: ""}[level == "error"], Message: map[bool]string{true: "failed safely", false: ""}[level == "error"]}
}

func TestFormatOperationalLifecycleEventUsesOnlySafeEnums(t *testing.T) {
	t.Parallel()
	event := diagnostics.Event{
		At: "2026-08-13T01:02:03Z", Level: "error", Component: "runtime",
		Event: "lifecycle.outcome", Result: "error", DurationMS: 7,
		RunID: "safe-run", Version: "0.10.0", MuxBackend: "tmux", Kind: "runtime",
		Operation: string(diagnostics.OperationSessionSwitch), Code: string(diagnostics.CodeSessionSwitchFailed),
	}
	got := formatOperationalEvent(event)
	for _, want := range []string{"operation=session.switch", "code=session.switch.failed", "run_id=safe-run"} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatted lifecycle = %q, want %q", got, want)
		}
	}
}

func TestFormatOperationalAIEventUsesOnlySafeEnums(t *testing.T) {
	t.Parallel()
	event := diagnostics.Event{
		At: "2026-08-14T01:02:03Z", Level: "error", Component: "ai",
		Event: "ai.ingest.outcome", Result: "error", DurationMS: 2,
		RunID: "safe-ai-run", Version: "0.10.0", MuxBackend: "tmux", Kind: "runtime",
		Provider: string(diagnostics.ProviderCodex), AIKind: string(diagnostics.AIKindPayload),
		AIResult: string(diagnostics.AIResultFailed), Failure: string(diagnostics.AIFailurePayloadInvalid),
	}
	got := formatOperationalEvent(event)
	for _, want := range []string{"provider=codex", "ai_kind=payload", "ai_result=failed", "failure=payload-invalid", "run_id=safe-ai-run"} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatted AI event = %q, want %q", got, want)
		}
	}
}

func TestFormatOperationalResourceEventUsesOnlySafeEnums(t *testing.T) {
	t.Parallel()
	event := diagnostics.Event{
		At: "2026-08-14T01:02:03Z", Level: "error", Component: "resource",
		Event: "resource.sampler.outcome", Result: "error", DurationMS: 2,
		RunID: "safe-resource-run", Version: "0.10.0", MuxBackend: "tmux", Kind: "runtime",
		Source: string(diagnostics.ResourceSourceInventory), ResourceResult: string(diagnostics.ResourceResultError), Failure: string(diagnostics.ResourceFailureInventory),
	}
	got := formatOperationalEvent(event)
	for _, want := range []string{"source=tmux-inventory", "resource_result=error", "failure=inventory-failed", "run_id=safe-resource-run"} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatted resource event = %q, want %q", got, want)
		}
	}
}

func TestDiagnosticsLogViewsSameReaderFixture(t *testing.T) {
	stateHome := t.TempDir()
	path := filepath.Join(stateHome, "projmux", "logs", diagnostics.LogFileName)
	store := diagnostics.NewStore(path)
	fixtures := []diagnostics.Event{
		diagnosticsFixture("one", "info", "cli"),
		diagnosticsFixture("two", "error", "runtime"),
		diagnosticsFixture("three", "error", "cli"),
	}
	for _, fixture := range fixtures {
		if err := store.Append(fixture); err != nil {
			t.Fatal(err)
		}
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString("malformed\n{\"truncated\"")
	_ = file.Close()

	cmd := &diagnosticsCommand{lookupEnv: func(name string) string {
		if name == "XDG_STATE_HOME" {
			return stateHome
		}
		return ""
	}, homeDir: func() (string, error) { return "/unused", nil }}

	var pathOut bytes.Buffer
	if err := cmd.Run([]string{"log", "--path"}, &pathOut, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(pathOut.String()); got != path {
		t.Fatalf("--path = %q, want %q", got, path)
	}

	var jsonOut bytes.Buffer
	if err := cmd.Run([]string{"log", "--json", "--level", "error", "--component", "cli", "--tail", "1"}, &jsonOut, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var got diagnostics.Event
	if err := json.Unmarshal(bytes.TrimSpace(jsonOut.Bytes()), &got); err != nil {
		t.Fatalf("JSON output = %q: %v", jsonOut.String(), err)
	}
	if got.RunID != "three" {
		t.Fatalf("filtered JSON run_id = %q", got.RunID)
	}

	var textOut bytes.Buffer
	if err := cmd.Run([]string{"log", "--level=error", "--component=cli", "--tail=1"}, &textOut, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"2026-08-12T01:02:03Z ERROR cli command.outcome error", "command=notify push", "run_id=three", "kind=runtime", "message=failed safely"} {
		if !strings.Contains(textOut.String(), want) {
			t.Fatalf("text output %q missing %q", textOut.String(), want)
		}
	}
}

func TestDiagnosticsLogMissingFileAndUsage(t *testing.T) {
	cmd := &diagnosticsCommand{lookupEnv: func(string) string { return t.TempDir() }, homeDir: os.UserHomeDir}
	var out bytes.Buffer
	if err := cmd.Run([]string{"log"}, &out, &bytes.Buffer{}); err != nil || out.Len() != 0 {
		t.Fatalf("missing log result out=%q err=%v", out.String(), err)
	}
	for _, args := range [][]string{{"log", "--tail=-1"}, {"log", "--level=debug"}, {"log", "extra"}, {"unknown"}} {
		if err := cmd.Run(args, &bytes.Buffer{}, &bytes.Buffer{}); err == nil || !IsUsageError(err) {
			t.Fatalf("Run(%q) err=%v, want UsageError", args, err)
		}
	}
}

func TestAppTopLevelDispatchesDiagnostics(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	var out bytes.Buffer
	if err := Run([]string{"diagnostics", "log", "--path"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(stateHome, "projmux", "logs", diagnostics.LogFileName)
	if strings.TrimSpace(out.String()) != want {
		t.Fatalf("path = %q, want %q", strings.TrimSpace(out.String()), want)
	}
}
