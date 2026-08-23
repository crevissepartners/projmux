package app

import (
	"context"
	"strings"

	intmux "github.com/crevissepartners/projmux/internal/integrations/mux"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

type codexLifecycleAuthorityDiagnostic struct {
	Source      string
	Reason      string
	EpochStatus string
}

type codexLifecycleAuthorityLookup func(string) codexLifecycleAuthorityDiagnostic

func defaultCodexLifecycleAuthorityLookup() codexLifecycleAuthorityLookup {
	runner := inttmux.ExecRunner{}
	return func(paneUID string) codexLifecycleAuthorityDiagnostic {
		return observeCodexLifecycleAuthority(context.Background(), runner, paneUID)
	}
}

func observeCodexLifecycleAuthority(ctx context.Context, runner tmuxRunner, paneUID string) codexLifecycleAuthorityDiagnostic {
	fallback := codexLifecycleAuthorityDiagnostic{Source: codexAuthorityHook, Reason: "no active native epoch", EpochStatus: "inactive"}
	if runner == nil || strings.TrimSpace(paneUID) == "" {
		return fallback
	}
	format := intmux.JoinFormats(intmux.FieldDelimiter, []string{
		intmux.PaneOptionFormat(tmuxopts.PaneUID),
		intmux.PaneOptionFormat(aiPaneCodexAuthorityOption),
		intmux.PaneOptionFormat(aiPaneCodexEpochOption),
		intmux.PaneOptionFormat(aiPaneCodexReasonOption),
	}...)
	out, err := runner.Run(ctx, "tmux", "list-panes", "-a", "-F", format)
	if err != nil {
		return codexLifecycleAuthorityDiagnostic{Source: "unavailable", Reason: "tmux observation failed", EpochStatus: "unknown"}
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		parts := strings.Split(line, intmux.FieldDelimiter)
		if len(parts) != 4 || strings.TrimSpace(parts[0]) != strings.TrimSpace(paneUID) {
			continue
		}
		source := safeCodexAuthorityValue(parts[1])
		reason := safeCodexAuthorityReason(parts[3])
		if source == codexAuthorityHook && strings.TrimSpace(parts[3]) == "" {
			reason = "no active native epoch"
		}
		epochStatus := "inactive"
		if strings.TrimSpace(parts[2]) != "" && (source == codexAuthorityControlPlane || source == codexAuthorityInvalidating) {
			epochStatus = "active"
		} else if source == codexAuthorityPending {
			epochStatus = "pending"
		}
		return codexLifecycleAuthorityDiagnostic{Source: source, Reason: reason, EpochStatus: epochStatus}
	}
	return fallback
}
