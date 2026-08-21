package usagecmd

import (
	"os"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/core/usage"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/version"
)

// journal.go records usage collection failures in the private operations
// journal, so a provider that stopped refreshing is discoverable after the
// fact through `projmux diagnostics log` rather than only in the moment the
// warning scrolled past on stderr.
//
// What reaches the journal is bounded by construction: the provider name and
// a closed failure enum. The adapter's own warning text — which is bounded but
// still upstream-shaped — is deliberately NOT copied into the record.

// recordCollectDiagnostics attributes a Manager collect result to the
// adapters that failed and appends one row per (provider, failure) tuple.
// A nil error records nothing: a successful collection produces zero rows.
//
// Journal writes are best-effort and intentionally have no return value —
// a failing journal must not change the result of the usage command.
func (c *Command) recordCollectDiagnostics(collectErr error, snapshots []usage.Snapshot, started time.Time) {
	if c == nil {
		return
	}
	adapterErrs := usage.AdapterErrors(collectErr)
	hasFallback := false
	for _, snapshot := range snapshots {
		if snapshot.FallbackReason != "" {
			hasFallback = true
			break
		}
	}
	if len(adapterErrs) == 0 && !hasFallback {
		return
	}
	journal := c.usageJournal()
	if journal == nil {
		return
	}
	for _, adapterErr := range adapterErrs {
		failure := diagnostics.UsageFailureCollect
		source := diagnostics.UsageSource("")
		if adapterErr.Partial() {
			failure = diagnostics.UsageFailureRowsSkipped
		}
		if snapshot, ok := usageDiagnosticSnapshot(snapshots, adapterErr.Model); ok {
			if adapterErr.Partial() {
				source = usageDiagnosticsSource(snapshot.Source)
			}
			if snapshot.StaleReason != "" {
				source = diagnostics.UsageSourceLastKnownGood
				failure = usageDiagnosticsReason(snapshot.StaleReason)
			}
		}
		journal.RecordCollectOutcome(usageDiagnosticsProvider(adapterErr.Model), source, failure, started)
	}
	seenFallback := map[string]bool{}
	for _, snapshot := range snapshots {
		model := strings.ToLower(strings.TrimSpace(snapshot.Model))
		if snapshot.FallbackReason == "" || snapshot.StaleReason != "" || seenFallback[model] {
			continue
		}
		seenFallback[model] = true
		journal.RecordCollectOutcome(
			usageDiagnosticsProvider(model), diagnostics.UsageSourceRollout,
			usageDiagnosticsReason(snapshot.FallbackReason), started,
		)
	}
}

func usageDiagnosticSnapshot(snapshots []usage.Snapshot, model string) (usage.Snapshot, bool) {
	for _, snapshot := range snapshots {
		if strings.EqualFold(strings.TrimSpace(snapshot.Model), strings.TrimSpace(model)) {
			return snapshot, true
		}
	}
	return usage.Snapshot{}, false
}

func usageDiagnosticsSource(source usage.SnapshotSource) diagnostics.UsageSource {
	switch source {
	case usage.SourceAppServer:
		return diagnostics.UsageSourceAppServer
	case usage.SourceRollout:
		return diagnostics.UsageSourceRollout
	default:
		return ""
	}
}

func usageDiagnosticsReason(reason usage.SnapshotReason) diagnostics.UsageFailure {
	switch reason {
	case usage.ReasonAppServerUnavailable:
		return diagnostics.UsageFailureAppServerUnavailable
	case usage.ReasonAppServerUnsupported:
		return diagnostics.UsageFailureAppServerUnsupported
	case usage.ReasonAccountUnsupported:
		return diagnostics.UsageFailureAccountUnsupported
	case usage.ReasonAppServerTimeout:
		return diagnostics.UsageFailureAppServerTimeout
	case usage.ReasonAppServerProtocol:
		return diagnostics.UsageFailureAppServerProtocol
	case usage.ReasonAppServerDisconnected:
		return diagnostics.UsageFailureAppServerDisconnected
	default:
		return diagnostics.UsageFailureCollect
	}
}

// usageJournal resolves the recorder once per Command. Resolution is lazy so
// a healthy run never touches the journal path at all.
func (c *Command) usageJournal() *diagnostics.UsageRecorder {
	if c.journal != nil {
		return c.journal
	}
	if c.journalFn != nil {
		c.journal = c.journalFn()
	} else {
		c.journal = c.defaultUsageJournal()
	}
	return c.journal
}

// defaultUsageJournal binds the shared private operations log. It returns nil
// when the journal path cannot be resolved, which degrades to "no journal
// rows" rather than failing the usage command.
func (c *Command) defaultUsageJournal() *diagnostics.UsageRecorder {
	path, err := diagnostics.DefaultPath(c.lookupEnv, os.UserHomeDir)
	if err != nil {
		return nil
	}
	return diagnostics.NewUsageRecorder(
		diagnostics.NewStore(path), diagnostics.NewRunID(), version.String(), diagnostics.MuxBackend())
}

// usageDiagnosticsProvider projects an adapter name onto the closed journal
// provider enum. Anything unrecognised becomes ProviderOther so an adapter
// name can never become free-form journal content.
func usageDiagnosticsProvider(model string) diagnostics.Provider {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "claude":
		return diagnostics.ProviderClaude
	case "codex":
		return diagnostics.ProviderCodex
	case "antigravity":
		return diagnostics.ProviderAntigravity
	default:
		return diagnostics.ProviderOther
	}
}
