package aisessions

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultResumeSummaryNativeBudget and DefaultResumeSummaryFallbackBudget
	// are the two declared bounds of one Codex summary discovery. They are
	// declared, never derived: the native page read is not the rollout scan's
	// prefix and the rollout scan is not the native read's remainder, so
	// neither bound is ever computed from time the other already spent.
	DefaultResumeSummaryNativeBudget   = 300 * time.Millisecond
	DefaultResumeSummaryFallbackBudget = 1250 * time.Millisecond

	// resumeSummaryCancellationSettlementBudget is not additional discovery
	// time. It is a bounded handoff window for a cancellation-aware fallback
	// that has already stopped scanning to publish its empty or partial result.
	// It stays below the app-level handoff bound, so an inner settlement always
	// completes inside the window the caller reserved for publishing a result.
	resumeSummaryCancellationSettlementBudget = 25 * time.Millisecond
)

// ResumeSummary is the list-only projection used by the Resume Picker. It
// deliberately excludes turns, runtime state, provenance explanations, and
// transcript/preview bytes. Invocation-local detail references are kept
// separately; they are never rendered, searched, or persisted.
type ResumeSummary struct {
	Provider             string
	ResumeID             string
	LastModified         time.Time
	UpdatedAt            time.Time
	Label                string
	Branch               string
	RelativeCWD          string
	Source               string
	StateDomainID        string
	EndpointGenerationID string
	GenerationState      string
}

// ResumeDetailRef is kept separately from ResumeSummary and crosses into the
// invocation-local preview boundary only after focus. It contains no preview
// or transcript bytes. transcriptPath is opaque outside aisessions.
type ResumeDetailRef struct {
	Provider             string
	ResumeID             string
	LastModified         time.Time
	UpdatedAt            time.Time
	Source               string
	StateDomainID        string
	EndpointGenerationID string
	GenerationState      string
	Turns                int
	Confidence           string
	Reason               string
	RuntimeStatus        string

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
// FallbackBudget applies only to the bounded rollout scan. The two are
// independent: neither is spent out of the other, and a zero value takes the
// declared default rather than whatever the caller context has left.
type ResumeSummaryOptions struct {
	DiscoverOptions
	NativeBudget   time.Duration
	FallbackBudget time.Duration

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
		// The rollout scan starts with the provider and owns its own bound. It is
		// the only remaining source once the native read fails or overruns, so a
		// native attempt that spends its whole bound must neither cancel it nor
		// shorten it to the time the caller context happens to have left.
		fallbackStage := startResumeSummaryStage(ctx, resumeSummaryBudget(opts.FallbackBudget, DefaultResumeSummaryFallbackBudget))
		defer fallbackStage.stop()
		fallback := make(chan []SessionMeta, 1)
		discoverFallback := opts.discoverCodexFallback
		if discoverFallback == nil {
			discoverFallback = discoverCodexContext
		}
		go func() {
			fallbackSessions := discoverFallback(fallbackStage.ctx, cwd, discoverOpts.CodexSessionsDir, depth)
			fallback <- finalizeProviderSessions(fallbackSessions, 0, true)
		}()
		if discoverOpts.OpenCodexCatalog != nil {
			budget := resumeSummaryBudget(opts.NativeBudget, DefaultResumeSummaryNativeBudget)
			native, hasMore, err := discoverCodexResumeSummaryBounded(ctx, cwd, depth, discoverOpts.OpenCodexCatalog, limit, budget)
			if err == nil {
				fallbackStage.stop()
				sessions = native
				moreNotLoaded = hasMore
				codexOutcome = CodexCatalogOutcome{Source: CatalogSourceNative, Confidence: CatalogConfidenceHigh}
				break
			}
			codexOutcome.Reason = codexCatalogFallbackReason(err)
		}
		if sessions == nil {
			// The selection waits on the rollout stage's own clock, not on the
			// caller's. Settling on the caller deadline is what let a slow native
			// or a slow route turn rows this invocation had already found into an
			// empty Codex result.
			select {
			case sessions = <-fallback:
			case <-fallbackStage.ctx.Done():
				fallbackStage.stop()
				// Cancellation asks the bounded scanner to stop, but its result send
				// races with the stage's Done. Give only that handoff a short bounded
				// window so a matching partial cannot become empty merely because the
				// send had not reached the buffered channel yet.
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

// resumeSummaryBudget resolves one declared stage bound. A non-positive value
// takes the declared default; it is never replaced by a sibling's remainder.
func resumeSummaryBudget(declared, fallback time.Duration) time.Duration {
	if declared > 0 {
		return declared
	}
	return fallback
}

// resumeSummaryStage is one discovery stage that owns its bound. The native
// page read and the rollout scan are siblings, not a chain: neither derives
// its context from the other, and neither inherits the time a caller context
// happens to have left. A caller deadline that has already been mostly spent,
// which is what a route that used its own bound before this discovery started
// leaves behind, must not shrink a stage below the bound declared for it:
// that is exactly how a usable rollout result gets discarded. A cancellation,
// meaning the invocation the caller closed, still terminates every stage at
// once, so nothing keeps scanning for a frame that will never be published.
type resumeSummaryStage struct {
	ctx  context.Context
	stop context.CancelFunc
}

func startResumeSummaryStage(parent context.Context, budget time.Duration) resumeSummaryStage {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), budget)
	parentDone := parent.Done()
	if parentDone == nil {
		return resumeSummaryStage{ctx: ctx, stop: cancel}
	}
	if resumeSummaryStageCanceled(parent) {
		// An invocation that is already closed starts no stage work at all;
		// deciding this here rather than from a watcher goroutine keeps a
		// closed invocation from racing one page read to completion.
		cancel()
		return resumeSummaryStage{ctx: ctx, stop: cancel}
	}
	released := make(chan struct{})
	var releaseOnce sync.Once
	go func() {
		select {
		case <-parentDone:
			if resumeSummaryStageCanceled(parent) {
				cancel()
			}
		case <-released:
		}
	}()
	return resumeSummaryStage{ctx: ctx, stop: func() {
		releaseOnce.Do(func() { close(released) })
		cancel()
	}}
}

// resumeSummaryStageCanceled separates an invocation the caller closed from a
// caller deadline, which only reports that some sibling stage spent its own
// bound. Only the former may end a stage before its own bound.
func resumeSummaryStageCanceled(parent context.Context) bool {
	return parent.Err() != nil && !errors.Is(context.Cause(parent), context.DeadlineExceeded)
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

// discoverCodexResumeSummaryBounded reads at most one native page on the
// native stage's own clock. parent contributes cancellation only: a caller
// whose own deadline is nearly spent must not reduce this read to a fraction
// of its declared bound, and this read must not consume the rollout scan's.
func discoverCodexResumeSummaryBounded(parent context.Context, cwd string, depth int, open OpenCodexCatalog, rowBudget int, budget time.Duration) ([]SessionMeta, bool, error) {
	stage := startResumeSummaryStage(parent, budget)
	defer stage.stop()
	ctx := stage.ctx
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

	var state *codexNativeCatalogState
	for {
		select {
		case state = <-opened:
			opened = nil
		case native := <-result:
			return native.sessions, native.hasMore, native.err
		case <-ctx.Done():
			// Actively close a blocked list so the endpoint does not keep a
			// connection open for a page this invocation will never publish.
			stage.stop()
			if state != nil {
				_ = state.close()
			}
			if errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
				return nil, false, context.DeadlineExceeded
			}
			return nil, false, ctx.Err()
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
		StateDomainID:        strings.TrimSpace(session.StateDomainID),
		EndpointGenerationID: strings.TrimSpace(session.EndpointGenerationID),
		GenerationState:      strings.TrimSpace(session.GenerationState),
	}
	ref := ResumeDetailRef{
		Provider: summary.Provider, ResumeID: summary.ResumeID, LastModified: summary.LastModified,
		UpdatedAt: summary.UpdatedAt, Source: summary.Source, Turns: session.Turns,
		StateDomainID: summary.StateDomainID, EndpointGenerationID: summary.EndpointGenerationID,
		GenerationState: summary.GenerationState,
		Confidence:      session.Confidence, Reason: session.Reason, RuntimeStatus: session.RuntimeStatus,
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
