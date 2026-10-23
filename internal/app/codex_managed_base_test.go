package app

import (
	"crypto/sha256"
	"fmt"
	"maps"
	"strings"
	"testing"

	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

// These expectations keep the new managed receipt explicit alongside each
// fixture's original exact metadata and disclosure assertions.
func confirmedCodexBaseAnnotations(previous map[string]string, text string) map[string]string {
	out := maps.Clone(previous)
	if out == nil {
		out = map[string]string{}
	}
	if text == "" {
		text = "\n"
	}
	out[mutationruntime.CodexManagedBaseDigest] = fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
	out[mutationruntime.CodexManagedBaseMode] = "base"
	out[mutationruntime.CodexManagedBaseStatus] = "base-delivered"
	return out
}

func unconfirmedCodexBaseAnnotations(previous map[string]string) map[string]string {
	out := maps.Clone(previous)
	if out == nil {
		out = map[string]string{}
	}
	out[mutationruntime.CodexManagedBaseStatus] = "base-unconfirmed"
	return out
}

func withoutManagedCodexResumeNotice(t *testing.T, stderr string) string {
	t.Helper()
	const notice = "codex-managed-base-not-refreshed: application of the current base override on the shared provider is unconfirmed"
	var kept []string
	sawNotice, sawReceipt := false, false
	for _, line := range strings.SplitAfter(stderr, "\n") {
		if strings.HasPrefix(line, "codex-managed-base-not-refreshed:") {
			if strings.TrimSpace(line) != notice || sawNotice {
				t.Fatalf("unexpected managed notice: %q", stderr)
			}
			sawNotice = true
			continue
		}
		if strings.HasPrefix(line, "Settings:") && strings.Contains(line, "latest managed base application unconfirmed") {
			for _, item := range []string{"instructions (", "agent-guidance (", "project-guidance (", "link-rules ("} {
				if !strings.Contains(line, item) {
					t.Fatalf("missing unconfirmed %s receipt: %q", item, stderr)
				}
			}
			if !strings.Contains(line, "not applied=4:") || sawReceipt {
				t.Fatalf("unexpected managed receipt: %q", stderr)
			}
			sawReceipt = true
			continue
		}
		kept = append(kept, line)
	}
	if sawNotice != sawReceipt {
		t.Fatalf("incomplete managed disclosure: %q", stderr)
	}
	return strings.Join(kept, "")
}

func assertFirstManagedCreateRefused(t *testing.T, result codexGuidanceResult) {
	t.Helper()
	if result.err == nil || !strings.Contains(result.err.Error(), "codex-managed-base-unavailable: no confirmed managed instructions; refusing provider defaults") {
		t.Fatalf("first managed create must explicitly refuse: %v", result.err)
	}
	if len(result.creates) != 0 || !result.unchanged {
		t.Fatalf("refusal changed Agent/pane or sent provider create: %+v", result)
	}
}
