package app

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestMaintainedDocsDoNotAdvertiseRetiredCLI protects current task and
// subsystem documentation, not just generated reference/help. Retirement and
// upgrade guides may quote removed argv to explain the breaking migration;
// explicitly marked historical records may preserve evidence from their time.
func TestMaintainedDocsDoNotAdvertiseRetiredCLI(t *testing.T) {
	t.Parallel()

	docsRoot := filepath.Join("..", "..", "docs")
	allowed := map[string]string{
		"legacy-cli-retirement.md": "# Legacy CLI Retirement Ledger",
		"upgrading.md":             "### Legacy compatibility routes removed",
		"migration-plan.md":        "> Historical migration record.",
		"notify-os-focus-poc.md":   "> **Status: retired.",
	}
	retiredCommand := regexp.MustCompile(`(?m)(?:^|[\x60\x22\x27=(])[ \t]*(?:\$[ \t]+)?projmux[ \t]+(?:ai(?:[ \t]|$)|attach[ \t]+auto(?:[ \t]|$)|current(?:[ \t]|$)|focus[ \t]+--(?:target|uri)(?:[ \t=]|$)|kill[ \t]+tagged(?:[ \t]|$)|sessions(?:[ \t]|$)|upgrade(?:[ \t]|$)|usage(?:[ \t]|$)|notify[ \t]+(?:push|list|ack|reconcile)(?:[ \t]|$)|pin[ \t]+(?:list|add|remove|toggle|clear)(?:[ \t]|$)|prune[ \t]+(?:ephemeral|session-state)(?:[ \t]|$)|session-state(?:[ \t]|$)|tag[ \t]+(?:list|toggle|clear|project)(?:[ \t]|$)|(?:key-broker|popup-wait-key|preview|session-popup|status|statusbar|tmux)(?:[ \t]|$))`)
	retiredTemplate := regexp.MustCompile(`(?:<projmux>|#\{q:projmux\})[\x22\x27} ]+(?:key-broker|popup-wait-key|preview|session-popup|status|statusbar|tmux)(?:[ \t]|$)`)
	legacyProducer := regexp.MustCompile(`(?:^|[^[:alnum:]_-])ai ingest (?:codex-hook|claude-hook|antigravity-hook|bell)(?:[ \t]|[\x60]|$)`)

	entries, err := os.ReadDir(docsRoot)
	if err != nil {
		t.Fatalf("read docs: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(docsRoot, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		text := string(raw)
		if entry.Name() != "legacy-cli-retirement.md" && entry.Name() != "upgrading.md" {
			if match := legacyProducer.FindString(text); match != "" {
				t.Errorf("%s repeats the exact legacy producer spelling %q outside the retirement ledger", entry.Name(), strings.TrimSpace(match))
			}
		}
		if marker, ok := allowed[entry.Name()]; ok {
			if !strings.Contains(text, marker) {
				t.Errorf("%s lost required retirement/historical marker %q", entry.Name(), marker)
			}
			continue
		}
		if match := retiredCommand.FindString(text); match != "" {
			t.Errorf("%s advertises retired executable argv %q", entry.Name(), strings.TrimSpace(match))
		}
		if match := retiredTemplate.FindString(text); match != "" {
			t.Errorf("%s advertises retired generated argv %q", entry.Name(), strings.TrimSpace(match))
		}
	}
}

// TestLegacyIngestCommandLiteralExistsOnlyInFixturesAndHistory makes the final
// removal a repository-wide assertion. Runtime migration readers intentionally
// still recognize marker-owned old bytes, but they construct that historical
// spelling from tokens; no executable producer, catalog, golden, or maintained
// documentation may carry the retired command literal.
func TestLegacyIngestCommandLiteralExistsOnlyInFixturesAndHistory(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")
	legacy := "projmux ai " + "ingest"
	allowedHistory := map[string]bool{
		"CHANGELOG.md":                  true,
		"docs/legacy-cli-retirement.md": true,
		"docs/upgrading.md":             true,
	}
	for _, rel := range trackedRepositoryFiles(t, root) {
		if allowedHistory[rel] || strings.HasSuffix(rel, "_test.go") || strings.Contains(rel, "/testdata/") {
			continue
		}
		path := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		var raw []byte
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				t.Fatal(err)
			}
			raw = []byte(target)
		} else {
			raw, err = os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
		}
		if strings.Contains(string(raw), legacy) {
			t.Errorf("%s contains the retired producer command outside a fixture/history file", rel)
		}
	}
}

// trackedRepositoryFiles keeps the literal boundary on committed source and
// generated artifacts. Build caches, worktree products, and local overlays are
// untracked by definition and are neither opened nor followed.
func trackedRepositoryFiles(t *testing.T, root string) []string {
	t.Helper()
	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("list tracked repository files: %v", err)
	}
	fields := bytes.Split(out, []byte{0})
	paths := make([]string, 0, len(fields))
	for _, field := range fields {
		if len(field) > 0 {
			paths = append(paths, filepath.ToSlash(string(field)))
		}
	}
	return paths
}

func TestProductionBinaryExcludesLegacyIngestCommandLiteral(t *testing.T) {
	root := filepath.Join("..", "..")
	binary := filepath.Join(t.TempDir(), "projmux")
	cmd := exec.Command("go", "build", "-o", binary, "./cmd/projmux")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build production binary: %v\n%s", err, output)
	}
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	legacy := []byte(strings.Join([]string{"projmux", "ai", "ingest"}, " "))
	if bytes.Contains(raw, legacy) {
		t.Fatalf("production binary contains retired producer bytes %q", legacy)
	}
}

func TestStaleUnmanagedLegacyHookRemediationIsExplicit(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "upgrading.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		"Stale unmanaged legacy hooks after the final removal",
		"does not rewrite them automatically",
		"unknown command: ai",
		"manually replace",
		"projmux internal agent-hook ingest",
		"projmux agent integrate <provider> --dry-run",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("upgrading guide is missing stale-unmanaged remediation %q", want)
		}
	}
}
