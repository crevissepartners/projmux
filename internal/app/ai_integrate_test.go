package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAIIntegrateCodexInstallsManagedNotify(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "codex"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate codex error = %v", err)
	}

	path := filepath.Join(home, codexConfigRelativePath)
	got := readCodexTestFile(t, path)
	if !strings.Contains(got, codexNotifyMarkerBegin) || !strings.Contains(got, codexNotifyLine) || !strings.Contains(got, codexNotifyMarkerEnd) {
		t.Fatalf("config missing managed block:\n%s", got)
	}
	if !strings.Contains(stdout.String(), "configured Codex legacy notify") {
		t.Fatalf("stdout = %q, want configured message", stdout.String())
	}
}

func TestAIIntegrateCodexInstallIsIdempotent(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile

	if err := cmd.Run([]string{"integrate", "codex"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("first Run integrate codex error = %v", err)
	}
	path := filepath.Join(home, codexConfigRelativePath)
	first := readCodexTestFile(t, path)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "codex"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("second Run integrate codex error = %v", err)
	}
	second := readCodexTestFile(t, path)
	if second != first {
		t.Fatalf("second install changed config:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	if !strings.Contains(stdout.String(), "no changes") {
		t.Fatalf("stdout = %q, want no changes", stdout.String())
	}
}

func TestAIIntegrateCodexDryRunDoesNotWrite(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "codex", "--dry-run"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate codex --dry-run error = %v", err)
	}

	path := filepath.Join(home, codexConfigRelativePath)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("config stat err = %v, want missing file", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "dry-run") || !strings.Contains(out, codexNotifyLine) {
		t.Fatalf("stdout = %q, want dry-run managed block preview", out)
	}
}

func TestAIIntegrateCodexRefusesUnmanagedNotify(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, codexConfigRelativePath)
	writeCodexTestFile(t, path, `model = "gpt-5.1-codex"
notify = ["custom", "notify"]
`)

	err := cmd.Run([]string{"integrate", "codex"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("Run integrate codex expected conflict error, got nil")
	}
	if !strings.Contains(err.Error(), "already configured outside a projmux-managed block") || !strings.Contains(err.Error(), "--dry-run") {
		t.Fatalf("error = %v, want conflict with dry-run guidance", err)
	}
	if got := readCodexTestFile(t, path); !strings.Contains(got, `notify = ["custom", "notify"]`) || strings.Contains(got, codexNotifyMarkerBegin) {
		t.Fatalf("config was modified unexpectedly:\n%s", got)
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "codex", "--dry-run"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate codex --dry-run with conflict error = %v", err)
	}
	if !strings.Contains(stdout.String(), "would refuse") || !strings.Contains(stdout.String(), `notify = ["custom", "notify"]`) {
		t.Fatalf("stdout = %q, want conflict preview", stdout.String())
	}
}

func TestAIIntegrateCodexRemoveOnlyManagedBlock(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, codexConfigRelativePath)
	writeCodexTestFile(t, path, codexNotifyBlock()+`model = "gpt-5.1-codex"
# keep this user setting
`)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "codex", "--remove"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate codex --remove error = %v", err)
	}
	got := readCodexTestFile(t, path)
	if strings.Contains(got, codexNotifyMarkerBegin) || !strings.Contains(got, `model = "gpt-5.1-codex"`) || !strings.Contains(got, "# keep this user setting") {
		t.Fatalf("config after remove =\n%s", got)
	}
	if !strings.Contains(stdout.String(), "removed projmux-managed") {
		t.Fatalf("stdout = %q, want removed message", stdout.String())
	}
}

func readCodexTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func writeCodexTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
