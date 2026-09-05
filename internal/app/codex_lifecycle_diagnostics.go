package app

import (
	"context"
	"slices"
	"strconv"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmux "github.com/crevissepartners/projmux/internal/integrations/mux"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

type codexLifecycleAuthorityDiagnostic struct {
	Source      string
	Reason      string
	EpochStatus string
	// Declared is the by-design reason this Agent has no native binding, from
	// the closed declared vocabulary, or empty. A hook observation with no
	// declared reason is an unexplained native fallback; one with a declared
	// reason is a lane that was chosen or gated on purpose.
	Declared string
	Dropped  uint32
	Unknown  uint32
	Overflow uint32
}

// unexplainedNativeFallback reports whether this Agent is on hook observation
// with nothing declaring why.
func (d codexLifecycleAuthorityDiagnostic) unexplainedNativeFallback() bool {
	return d.Source == codexAuthorityHook && d.Declared == ""
}

// codexAuthorityCensus is the closed classification of every managed Codex
// Agent's current lifecycle authority. It is a count-only projection: it names
// no Agent, so it is safe on every diagnostics surface.
type codexAuthorityCensus struct {
	Agents       int `json:"agents"`
	ControlPlane int `json:"control_plane"`
	Pending      int `json:"pending"`
	Invalidating int `json:"invalidating"`
	// DeclaredHook counts Agents on hook observation with a declared reason.
	DeclaredHook int `json:"declared_hook"`
	// PayloadFreeFallback is the declared-hook subset created by the permanent
	// pre-provider payload-free fallback. It is a count-only, content-free
	// reduced-native-control signal.
	PayloadFreeFallback int `json:"payload_free_fallback,omitempty"`
	// UnexplainedHook counts Agents on hook observation with no declared
	// reason. This is the number the native-authority contract requires to be
	// zero for Agents that were created with a payload.
	UnexplainedHook int `json:"unexplained_hook"`
	Unavailable     int `json:"unavailable"`
	// Reasons is the distribution of bounded authority reasons across the
	// same Agents, ordered by token.
	//
	// The source counts above cannot separate the three states an operator
	// actually needs to tell apart. A flapping observer, one frozen at its
	// first epoch, and one that stopped after flapping all land in the same
	// invalidating bucket; what differs is the token that put them there.
	Reasons []codexAuthorityReasonCount `json:"reasons,omitempty"`
}

// codexAuthorityReasonCount is one bounded reason token and how many managed
// Codex Agents currently carry it. It names no Agent, so it stays safe on
// every diagnostics surface for the same reason the counts above do.
type codexAuthorityReasonCount struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// censusCodexLifecycleAuthority classifies every managed Codex Agent in one
// Registry snapshot.
func censusCodexLifecycleAuthority(
	registry coremetadata.Registry,
	lookup codexLifecycleAuthorityLookup,
) codexAuthorityCensus {
	census := codexAuthorityCensus{}
	if lookup == nil {
		return census
	}
	reasons := map[string]int{}
	for _, agent := range registry.Agents {
		if agent.Spec.Provider != aiModeCodex || agent.Status.PaneRef == "" {
			continue
		}
		census.Agents++
		diagnostic := lookup(agent.Status.PaneRef)
		if reason := strings.TrimSpace(diagnostic.Reason); reason != "" {
			reasons[reason]++
		}
		switch diagnostic.Source {
		case codexAuthorityControlPlane:
			census.ControlPlane++
		case codexAuthorityPending:
			census.Pending++
		case codexAuthorityInvalidating:
			census.Invalidating++
		case codexAuthorityHook:
			if diagnostic.unexplainedNativeFallback() {
				census.UnexplainedHook++
			} else {
				census.DeclaredHook++
				if diagnostic.Declared == codexNativeDeclaredPayloadFreeFallback {
					census.PayloadFreeFallback++
				}
			}
		default:
			census.Unavailable++
		}
	}
	census.Reasons = codexAuthorityReasonCounts(reasons)
	return census
}

// codexAuthorityReasonCounts orders the distribution by token so two doctor
// runs over the same state render identically.
func codexAuthorityReasonCounts(counts map[string]int) []codexAuthorityReasonCount {
	if len(counts) == 0 {
		return nil
	}
	ordered := make([]codexAuthorityReasonCount, 0, len(counts))
	for reason, count := range counts {
		ordered = append(ordered, codexAuthorityReasonCount{Reason: reason, Count: count})
	}
	slices.SortFunc(ordered, func(a, b codexAuthorityReasonCount) int {
		return strings.Compare(a.Reason, b.Reason)
	})
	return ordered
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
		intmux.PaneOptionFormat(aiPaneCodexDroppedOption),
		intmux.PaneOptionFormat(aiPaneCodexUnknownOption),
		intmux.PaneOptionFormat(aiPaneCodexOverflowOption),
		intmux.PaneOptionFormat(aiPaneCodexDeclaredOption),
	}...)
	out, err := runner.Run(ctx, "tmux", "list-panes", "-a", "-F", format)
	if err != nil {
		return codexLifecycleAuthorityDiagnostic{Source: "unavailable", Reason: "tmux observation failed", EpochStatus: "unknown"}
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		parts := strings.Split(line, intmux.FieldDelimiter)
		if len(parts) < 4 || strings.TrimSpace(parts[0]) != strings.TrimSpace(paneUID) {
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
		diagnostic := codexLifecycleAuthorityDiagnostic{Source: source, Reason: reason, EpochStatus: epochStatus}
		if len(parts) >= 7 {
			diagnostic.Dropped = safeProgressCounter(parts[4])
			diagnostic.Unknown = safeProgressCounter(parts[5])
			diagnostic.Overflow = safeProgressCounter(parts[6])
		}
		if len(parts) >= 8 {
			// The vocabulary check is what keeps a stale or hand-written pane
			// option from declaring an unexplained fallback away.
			diagnostic.Declared = codexNativeDeclaredReason(parts[7])
		}
		return diagnostic
	}
	return fallback
}

func safeProgressCounter(value string) uint32 {
	parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 32)
	if err != nil {
		return 0
	}
	return uint32(parsed)
}
