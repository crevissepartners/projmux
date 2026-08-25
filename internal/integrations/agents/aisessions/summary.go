package aisessions

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"
)

const (
	DefaultResumeSummaryNativeBudget = 300 * time.Millisecond

	// resumeSummaryCancellationSettlementBudget is not additional discovery
	// time. It is a bounded handoff window for a cancellation-aware fallback
	// that has already stopped scanning to publish its empty or partial result.
	// The app keeps this inside the <500ms first-frame contract.
	resumeSummaryCancellationSettlementBudget = 25 * time.Millisecond
)

// ResumeSummary is the list-only projection used by the Resume Picker. It
// deliberately excludes turns, runtime state, provenance explanations, and
// transcript/preview bytes. Invocation-local detail references are kept
// separately; they are never rendered, searched, or persisted.
type ResumeSummary struct {
	Provider     string
	ResumeID     string
	LastModified time.Time
	UpdatedAt    time.Time
	Label        string
	Branch       string
	RelativeCWD  string
	Source       string
}

// ResumeDetailRef is kept separately from ResumeSummary and crosses into the
// invocation-local preview boundary only after focus. It contains no preview
// or transcript bytes. transcriptPath is opaque outside aisessions.
type ResumeDetailRef struct {
	Provider      string
	ResumeID      string
	LastModified  time.Time
	UpdatedAt     time.Time
	Source        string
	Turns         int
	Confidence    string
	Reason        string
	RuntimeStatus string

	transcriptPath string
}

// ResumeDetail is the invocation-local projection loaded for one exact
// selection. Preview bytes and deferred turn counts never cross back into the
// summary/list projection.
type ResumeDetail struct {
	Turns         int
	Source        string
	Confidence    string
	Reason        string
	RuntimeStatus string
	Preview       Preview
}

// ResumeSummaryOptions controls the provider-local bounded population seam.
// NativeBudget applies only to the Codex native attempt; a timeout selects the
// rollout summary for this invocation and any late native return is discarded.
type ResumeSummaryOptions struct {
	DiscoverOptions
	NativeBudget time.Duration

	// discoverCodexFallback is a package-private test seam for coordinating the
	// native/fallback race without weakening the production filesystem path.
	discoverCodexFallback func(context.Context, string, string, int) []SessionMeta
}

// ResumeSummaryDiscovery is one provider's settled list projection. Codex is
// zero-valued for other providers. MoreNotLoaded is content-free chrome state.
type ResumeSummaryDiscovery struct {
	Summaries     []ResumeSummary
	DetailRefs    []ResumeDetailRef
	Codex         CodexCatalogOutcome
	MoreNotLoaded bool
}

// DiscoverResumeSummariesContext returns one settled provider result without
// turn enrichment, preview reads, or Codex continuation. Codex native summary
// population reads at most one page inside NativeBudget while the bounded,
// cancellation-aware rollout fallback scans concurrently. Native success,
// including native-empty, keeps authority. A native failure or budget overrun
// selects the already-running fallback and discards every late native result.
func DiscoverResumeSummariesContext(ctx context.Context, provider, cwd string, opts ResumeSummaryOptions, limit int) (ResumeSummaryDiscovery, error) {
	cwd = cleanCWD(cwd)
	if cwd == "" {
		return ResumeSummaryDiscovery{}, errors.New("discover ai resume summaries: cwd is empty")
	}
	discoverOpts := opts.DiscoverOptions.withDefaults()
	discoverOpts.DeferTurns = true
	depth := max(discoverOpts.Depth, 0)
	provider = strings.ToLower(strings.TrimSpace(provider))

	var sessions []SessionMeta
	var codexOutcome CodexCatalogOutcome
	var moreNotLoaded bool
	switch provider {
	case AgentClaude:
		discovery, err := DiscoverProviderContext(ctx, provider, cwd, discoverOpts, 0)
		if err != nil {
			return ResumeSummaryDiscovery{}, err
		}
		sessions = discovery.Sessions
	case AgentCodex:
		codexOutcome = CodexCatalogOutcome{Source: CatalogSourceFallback, Confidence: CatalogConfidenceMedium}
		fallbackCtx, cancelFallback := context.WithCancel(ctx)
		defer cancelFallback()
		fallback := make(chan []SessionMeta, 1)
		discoverFallback := opts.discoverCodexFallback
		if discoverFallback == nil {
			discoverFallback = discoverCodexContext
		}
		go func() {
			fallbackSessions := discoverFallback(fallbackCtx, cwd, discoverOpts.CodexSessionsDir, depth)
			fallback <- finalizeProviderSessions(fallbackSessions, 0, true)
		}()
		if discoverOpts.OpenCodexCatalog != nil {
			budget := opts.NativeBudget
			if budget <= 0 {
				budget = DefaultResumeSummaryNativeBudget
			}
			native, hasMore, err := discoverCodexResumeSummaryBounded(ctx, cwd, depth, discoverOpts.OpenCodexCatalog, limit, budget)
			if err == nil {
				cancelFallback()
				sessions = native
				moreNotLoaded = hasMore
				codexOutcome = CodexCatalogOutcome{Source: CatalogSourceNative, Confidence: CatalogConfidenceHigh}
				break
			}
			codexOutcome.Reason = codexCatalogFallbackReason(err)
		}
		if sessions == nil {
			select {
			case sessions = <-fallback:
			case <-ctx.Done():
				cancelFallback()
				// Cancellation asks the bounded scanner to stop, but its result send
				// races with ctx.Done. Give only that handoff a short bounded window
				// so a matching partial cannot become empty merely because the send
				// had not reached the buffered channel yet.
				sessions = settleCanceledCodexFallback(fallback)
			}
			for i := range sessions {
				sessions[i].Confidence = ConfidenceMedium
				sessions[i].Reason = string(codexOutcome.Reason)
			}
		}
	case AgentAntigravity:
		discovery, err := DiscoverProviderContext(ctx, provider, cwd, discoverOpts, 0)
		if err != nil {
			return ResumeSummaryDiscovery{}, err
		}
		sessions = discovery.Sessions
	default:
		return ResumeSummaryDiscovery{}, errors.New("unsupported resume summary provider " + provider)
	}

	summaries := make([]ResumeSummary, 0, len(sessions))
	detailRefs := make([]ResumeDetailRef, 0, len(sessions))
	for _, session := range sessions {
		summary, detailRef := projectResumeSummary(session, cwd, depth)
		summaries = append(summaries, summary)
		detailRefs = append(detailRefs, detailRef)
	}
	return ResumeSummaryDiscovery{Summaries: summaries, DetailRefs: detailRefs, Codex: codexOutcome, MoreNotLoaded: moreNotLoaded}, nil
}

func settleCanceledCodexFallback(fallback <-chan []SessionMeta) []SessionMeta {
	timer := time.NewTimer(resumeSummaryCancellationSettlementBudget)
	defer timer.Stop()
	select {
	case sessions := <-fallback:
		return sessions
	case <-timer.C:
		return []SessionMeta{}
	}
}

type resumeSummaryNativeResult struct {
	sessions []SessionMeta
	hasMore  bool
	err      error
}

func discoverCodexResumeSummaryBounded(parent context.Context, cwd string, depth int, open OpenCodexCatalog, rowBudget int, budget time.Duration) ([]SessionMeta, bool, error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	result := make(chan resumeSummaryNativeResult, 1)
	opened := make(chan *codexNativeCatalogState, 1)
	go func() {
		state, err := openCodexNativeCatalogState(ctx, cwd, depth, open)
		if err != nil {
			result <- resumeSummaryNativeResult{err: err}
			return
		}
		opened <- state
		if err := ctx.Err(); err != nil {
			_ = state.close()
			result <- resumeSummaryNativeResult{err: err}
			return
		}
		hasMore, err := state.readPage(ctx, cwd, depth)
		sessions := state.sessions()
		if rowBudget > 0 && len(sessions) > rowBudget {
			sessions = sessions[:rowBudget]
			hasMore = true
		}
		_ = state.close()
		result <- resumeSummaryNativeResult{sessions: sessions, hasMore: hasMore, err: err}
	}()

	timer := time.NewTimer(budget)
	defer timer.Stop()
	var state *codexNativeCatalogState
	for {
		select {
		case state = <-opened:
			opened = nil
		case native := <-result:
			return native.sessions, native.hasMore, native.err
		case <-timer.C:
			cancel()
			if state != nil {
				_ = state.close()
			}
			return nil, false, context.DeadlineExceeded
		case <-parent.Done():
			cancel()
			if state != nil {
				_ = state.close()
			}
			return nil, false, parent.Err()
		}
	}
}

func projectResumeSummary(session SessionMeta, baseCWD string, depth int) (ResumeSummary, ResumeDetailRef) {
	relativeCWD := ""
	if depth > 0 {
		if rel, err := filepath.Rel(baseCWD, cleanCWD(session.Context.CWD)); err == nil {
			rel = filepath.ToSlash(rel)
			if rel == "." {
				relativeCWD = "./"
			} else if rel != ".." && !strings.HasPrefix(rel, "../") {
				relativeCWD = "./" + rel
			}
		}
	}
	summary := ResumeSummary{
		Provider: strings.TrimSpace(session.Agent), ResumeID: strings.TrimSpace(session.ResumeID),
		LastModified: session.LastModified, UpdatedAt: session.UpdatedAt,
		Label: strings.Join(strings.Fields(session.Title), " "), Branch: strings.TrimSpace(session.Context.Branch),
		RelativeCWD: relativeCWD, Source: strings.TrimSpace(session.Source),
	}
	ref := ResumeDetailRef{
		Provider: summary.Provider, ResumeID: summary.ResumeID, LastModified: summary.LastModified,
		UpdatedAt: summary.UpdatedAt, Source: summary.Source, Turns: session.Turns,
		Confidence: session.Confidence, Reason: session.Reason, RuntimeStatus: session.RuntimeStatus,
		transcriptPath: session.sourcePath,
	}
	return summary, ref
}

// ReadResumeDetail crosses the exact selection boundary once. It combines
// non-summary metadata, a deferred turn count when a local transcript exists,
// and the existing bounded preview projection without persisting any result.
func ReadResumeDetail(ctx context.Context, ref ResumeDetailRef, open OpenCodexCatalog) (ResumeDetail, error) {
	session := SessionMeta{
		Agent: ref.Provider, ResumeID: ref.ResumeID, LastModified: ref.LastModified,
		UpdatedAt: ref.UpdatedAt, Source: ref.Source, Turns: ref.Turns,
		Confidence: ref.Confidence, Reason: ref.Reason, RuntimeStatus: ref.RuntimeStatus,
		sourcePath: ref.transcriptPath,
	}
	detail := ResumeDetail{
		Turns: session.Turns, Source: session.Source, Confidence: session.Confidence,
		Reason: session.Reason, RuntimeStatus: session.RuntimeStatus,
	}
	if session.sourcePath != "" {
		if turns, ok := countUserTurnsContext(ctx, session.sourcePath); ok {
			detail.Turns = turns
		}
	}
	preview, err := ReadPreview(ctx, session, open)
	detail.Preview = preview
	return detail, err
}
