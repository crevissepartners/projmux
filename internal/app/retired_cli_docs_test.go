package app

import (
	"os"
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
		if entry.Name() != "legacy-cli-retirement.md" {
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
