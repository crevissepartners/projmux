package runtime

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	"github.com/crevissepartners/projmux/internal/version"
)

// ResumeSummaryPopulationBudget is the discovery envelope of every provider
// that does not resolve Codex routes.
const ResumeSummaryPopulationBudget = 450 * time.Millisecond

// ResumePickerLimitDefault is the number of recent sessions a resume list
// shows when nothing is configured.
const ResumePickerLimitDefault = 30

// NormalizeResumePickerLimit maps a raw limit to the value the list actually
// applies: non-positive falls back to the default, oversized clamps to the
// supported maximum, and in-range values pass through unchanged.
func NormalizeResumePickerLimit(limit int) int {
	if limit <= 0 {
		return ResumePickerLimitDefault
	}
	if limit > hooks.AIResumePickerLimitMax {
		return hooks.AIResumePickerLimitMax
	}
	return limit
}

// normalizeAIMode is the provider spelling the resume rows are keyed by.
func normalizeAIMode(mode string) string {
	if valid, ok := config.ValidAINewWindowMode(mode); ok {
		return valid
	}
	return "selective"
}

// ResumeProviderResult is one provider's settled discovery.
type ResumeProviderResult struct {
	Provider        string
	Discovery       aisessions.ResumeSummaryDiscovery
	Routes          []NativeCodexRoute
	Err             error
	EnvelopeExpired bool
	// Elapsed is this provider's own witness, measured from this provider's
	// start instant. It is never derived from the frame's start or from a
	// sibling's span, and it never leaves this settlement.
	Elapsed time.Duration
}

// ResumeProviderState is the settled state of one provider.
type ResumeProviderState string

const (
	ResumeProviderCount          ResumeProviderState = "count"
	ResumeProviderSearchFailed   ResumeProviderState = "search_failed"
	ResumeProviderScanUnfinished ResumeProviderState = "scan_unfinished"
	ResumeProviderDisabled       ResumeProviderState = "disabled"
)

// ResumeCodexCatalog is the Codex native generation inventory a discovery
// reads routes from.
type ResumeCodexCatalog interface {
	CatalogRoutes(context.Context) ([]NativeCodexRoute, error)
}

// resumeDiscoverySources are the provider and Registry reads one discovery
// uses.
type resumeDiscoverySources struct {
	codexNative                   ResumeCodexCatalog
	openCodexCatalog              aisessions.OpenCodexCatalog
	loadRegistry                  func() (coremetadata.Registry, error)
	discoverResumeSummaryProvider func(context.Context, string, string, aisessions.ResumeSummaryOptions, int) (aisessions.ResumeSummaryDiscovery, error)
	openRoute                     func(context.Context, NativeCodexRoute, bool) (*codexappserver.Client, error)
}

// ResumeDiscoveryOptions configures one resume candidate discovery. Ctx is the
// invocation lifetime; every provider envelope and stage span starts under it.
// CodexNative nil reads Codex without native routes. Discover nil uses the
// installed summary discovery. OpenRoute nil connects to a route with
// OpenNativeCodexRoute. RecordElapsed, when set, receives each provider's
// settlement witness at that provider's own terminal.
type ResumeDiscoveryOptions struct {
	Ctx               context.Context
	Cwd, Home         string
	Depth, Limit      int
	ProviderEnabled   map[string]bool
	PopulationTimeout time.Duration
	PopulationContext func(context.Context, time.Duration) (context.Context, context.CancelFunc)
	Budgets           ResumeBudgets
	HandoffTimer      func(time.Duration) (<-chan time.Time, func())
	CodexNative       ResumeCodexCatalog
	OpenRoute         func(context.Context, NativeCodexRoute, bool) (*codexappserver.Client, error)
	OpenCodexCatalog  aisessions.OpenCodexCatalog
	LoadRegistry      func() (coremetadata.Registry, error)
	Discover          func(context.Context, string, string, aisessions.ResumeSummaryOptions, int) (aisessions.ResumeSummaryDiscovery, error)
	RecordElapsed     func(provider string, elapsed time.Duration)
}

// ResumeDiscovery settles the resume candidates of Codex, Claude and
// Antigravity, each provider on its own clock.
type ResumeDiscovery struct {
	cmd               *resumeDiscoverySources
	ctx               context.Context
	cwd               string
	home              string
	depth             int
	limit             int
	providerEnabled   map[string]bool
	populationTimeout time.Duration
	populationContext func(context.Context, time.Duration) (context.Context, context.CancelFunc)
	budgets           ResumeBudgets
	handoffTimer      func(time.Duration) (<-chan time.Time, func())
	recordElapsed     func(provider string, elapsed time.Duration)
}

// NewResumeDiscovery returns the discovery opts describes.
func NewResumeDiscovery(opts ResumeDiscoveryOptions) *ResumeDiscovery {
	ctx := opts.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return &ResumeDiscovery{
		cmd:               &resumeDiscoverySources{codexNative: opts.CodexNative, openCodexCatalog: opts.OpenCodexCatalog, loadRegistry: opts.LoadRegistry, discoverResumeSummaryProvider: opts.Discover, openRoute: opts.OpenRoute},
		ctx:               ctx,
		cwd:               opts.Cwd,
		home:              opts.Home,
		depth:             opts.Depth,
		limit:             NormalizeResumePickerLimit(opts.Limit),
		providerEnabled:   opts.ProviderEnabled,
		populationTimeout: opts.PopulationTimeout,
		populationContext: opts.PopulationContext,
		budgets:           opts.Budgets,
		handoffTimer:      opts.HandoffTimer,
		recordElapsed:     opts.RecordElapsed,
	}
}

// ResumeFrame is one settled discovery: the capped rows of every provider
// whose state is count, their detail refs, the Codex routes those rows were
// read through, and each provider's settled result.
type ResumeFrame struct {
	Summaries     []aisessions.ResumeSummary
	DetailRefs    map[string]aisessions.ResumeDetailRef
	Routes        []NativeCodexRoute
	Results       map[string]ResumeProviderResult
	MoreNotLoaded bool
}

// Populate settles all provider summaries in one frame. Provider completion
// order and late returns never append, reorder, or replace rows of the frame
// this call returns.
func (c *ResumeDiscovery) Populate() ResumeFrame {
	timeout := c.populationTimeout
	if timeout <= 0 {
		timeout = ResumeSummaryPopulationBudget
	}
	withTimeout := c.populationContext
	if withTimeout == nil {
		withTimeout = context.WithTimeout
	}
	discover := c.cmd.discoverResumeSummaryProvider
	if discover == nil {
		discover = aisessions.DiscoverResumeSummariesContext
	}
	providers := []string{aiModeCodex, aiModeClaude, aiModeAntigravity}
	results := make(chan ResumeProviderResult, len(providers))
	settled := make(map[string]ResumeProviderResult, len(providers))
	pending := 0
	for _, provider := range providers {
		if !c.providerEnabled[provider] {
			settled[provider] = ResumeProviderResult{Provider: provider}
			continue
		}
		// Every enabled provider opens its own envelope here, at its own start
		// instant, with its own declared bound. There is no shared discovery
		// deadline left to inherit, so one provider being slow, failing, or not
		// being configured at all cannot move where a sibling terminalizes.
		envelope, stop := withTimeout(c.ctx, c.providerEnvelope(provider, timeout))
		startedAt := c.budgets.Instant()
		defer stop()
		pending++
		go func() {
			returned := make(chan ResumeProviderResult, 1)
			go func() {
				discovery, routes, err := c.discoverResumeProvider(envelope, discover, provider)
				returned <- ResumeProviderResult{Provider: provider, Discovery: discovery, Routes: routes, Err: err}
			}()
			result := c.SettleProviderResult(provider, envelope, startedAt, returned)
			c.recordProviderElapsed(provider, result.Elapsed)
			results <- result
		}()
	}

	// Each goroutine above terminalizes on its own clock and then sends exactly
	// one result, so collecting them needs no envelope of its own: the frame is
	// the set of provider terminals, not a deadline that overrides them.
	for range pending {
		result := <-results
		settled[result.Provider] = result
	}

	var summaries []aisessions.ResumeSummary
	var routes []NativeCodexRoute
	allDetailRefs := make(map[string]aisessions.ResumeDetailRef)
	moreNotLoaded := false
	for _, provider := range providers {
		result := settled[provider]
		// The rows published for a provider come from the same settled result
		// its state and count are read from, so a provider can never report
		// rows it did not contribute or contribute rows it did not report.
		if state, _ := ResumeProviderOutcome(result, c.providerEnabled[provider]); state == ResumeProviderCount {
			summaries = append(summaries, result.Discovery.Summaries...)
			routes = append(routes, result.Routes...)
			for _, ref := range result.Discovery.DetailRefs {
				key := normalizeAIMode(ref.Provider) + "\x00" + strings.TrimSpace(ref.ResumeID)
				if old, ok := allDetailRefs[key]; !ok || ref.LastModified.After(old.LastModified) {
					allDetailRefs[key] = ref
				}
			}
			moreNotLoaded = moreNotLoaded || result.Discovery.MoreNotLoaded
		}
	}
	// One global dedupe/sort/cap pass over the settled rows of all three
	// providers. The provider counts above are already fixed and stay pre-cap.
	summaries, capped := SettleResumeSummaries(summaries, c.limit)
	detailRefs := make(map[string]aisessions.ResumeDetailRef, len(summaries))
	for _, summary := range summaries {
		key := normalizeAIMode(summary.Provider) + "\x00" + strings.TrimSpace(summary.ResumeID)
		if ref, ok := allDetailRefs[key]; ok {
			detailRefs[key] = ref
		}
	}
	return ResumeFrame{Summaries: summaries, DetailRefs: detailRefs, Routes: routes, Results: settled, MoreNotLoaded: moreNotLoaded || capped}
}

// providerEnvelope is one provider's own declared bound. Codex resolves routes
// and a bounded native page on its own staged clocks, so its envelope is the
// declared provider terminal; every other provider keeps the population
// budget. The bound belongs to the provider: whether Codex is configured, and
// how long it takes, never changes what Claude or Antigravity are given.
func (c *ResumeDiscovery) providerEnvelope(provider string, timeout time.Duration) time.Duration {
	if provider == aiModeCodex && c.codexBudgetStagesActive() {
		return max(timeout, c.budgets.ProviderTerminal())
	}
	return timeout
}

// SettleProviderResult terminalizes one provider on that provider's own clock.
// The envelope bound and the handoff window that follows it are both measured
// from this provider's start instant, so a sibling still resolving neither
// expires this result nor extends it.
func (c *ResumeDiscovery) SettleProviderResult(
	provider string,
	envelope context.Context,
	startedAt time.Time,
	returned <-chan ResumeProviderResult,
) ResumeProviderResult {
	select {
	case result := <-returned:
		result.Elapsed = c.providerElapsedSince(startedAt)
		return result
	case <-envelope.Done():
	}
	// Provider cancellation and its buffered result send are separate scheduler
	// events. Reserve the declared handoff window after this provider's own
	// envelope so a cancellation-settled partial is not replaced by
	// available-empty solely because its send lost the select race.
	newTimer := c.handoffTimer
	if newTimer == nil {
		newTimer = func(budget time.Duration) (<-chan time.Time, func()) {
			timer := time.NewTimer(budget)
			return timer.C, func() { timer.Stop() }
		}
	}
	handoff, stop := newTimer(c.budgets.Stage(ResumeStageHandoff))
	defer stop()
	select {
	case result := <-returned:
		result.EnvelopeExpired = errors.Is(result.Err, context.Canceled) || errors.Is(result.Err, context.DeadlineExceeded)
		result.Elapsed = c.providerElapsedSince(startedAt)
		return result
	case <-handoff:
		return ResumeProviderResult{Provider: provider, EnvelopeExpired: true, Elapsed: c.providerElapsedSince(startedAt)}
	}
}

// recordProviderElapsed publishes one provider's settlement witness at that
// provider's own terminal rather than at the frame's, which is what makes the
// value a provider clock instead of a frame clock. C-4 keeps it internal: it
// is not a row, a value, a footer string, or a Registry write, and nothing
// user-visible reads it.
func (c *ResumeDiscovery) recordProviderElapsed(provider string, elapsed time.Duration) {
	if c.recordElapsed != nil {
		c.recordElapsed(provider, elapsed)
	}
}

// providerElapsedSince reads the budget clock for one provider witness. The
// instant it subtracts is that provider's own start, never the frame's and
// never a sibling stage's.
func (c *ResumeDiscovery) providerElapsedSince(startedAt time.Time) time.Duration {
	elapsed := c.budgets.Instant().Sub(startedAt)
	if elapsed < 0 {
		return 0
	}
	return elapsed
}

// codexBudgetStagesActive reports whether this invocation resolves Codex
// routes, which is the only path that owns the staged budgets.
func (c *ResumeDiscovery) codexBudgetStagesActive() bool {
	return c.providerEnabled[aiModeCodex] && c.cmd != nil && c.cmd.codexNative != nil
}

// catalogRoutesWithinBudget resolves the generation inventory on the route
// clock alone, started under the invocation lifetime rather than under the
// provider discovery envelope. CatalogRoutes is a read-only projection, so a
// return that arrives after the route bound is discarded: the span has already
// produced the one terminal reason for this invocation.
func (c *ResumeDiscovery) catalogRoutesWithinBudget() ([]NativeCodexRoute, error) {
	span := c.budgets.Start(c.ctx, ResumeStageRoute)
	defer span.Stop()
	type routeInventory struct {
		routes []NativeCodexRoute
		err    error
	}
	// Buffered so a late CatalogRoutes return neither blocks its goroutine nor
	// reaches a settled invocation.
	resolved := make(chan routeInventory, 1)
	go func() {
		routes, err := c.cmd.codexNative.CatalogRoutes(span.Ctx)
		resolved <- routeInventory{routes: routes, err: err}
	}()
	select {
	case inventory := <-resolved:
		return inventory.routes, inventory.err
	case <-span.Ctx.Done():
		// Cause separates "this stage spent its own bound" from "the invocation
		// was closed"; only the former is a route budget timeout.
		if errors.Is(context.Cause(span.Ctx), context.DeadlineExceeded) {
			return nil, c.budgets.Timeout(span)
		}
		return nil, span.Ctx.Err()
	}
}

// ResumeCodexRouteReadRolloutBudget is the rollout bound a per-route native read
// declares for itself. This invocation enters the rollout scan exactly once,
// with the provider, under fallbackSpan. A route read that opened a scan of its
// own would enter a second one and start that one at route_end: the very
// start instant this stage exists to remove, and a bound the declared provider
// terminal does not include. ResumeSummaryOptions spells "no scan of my own" as
// an already spent positive bound, because a zero value selects the declared
// default instead.
const ResumeCodexRouteReadRolloutBudget = time.Nanosecond

// errCodexRouteReadOwnsNativeCatalog refuses a native endpoint to the rollout
// scan's own catalog attempt. The scan is source-only: the endpoint named by a
// resolved route belongs to the route reads, which start when the route has
// actually resolved and own the native bound from that instant.
var errCodexRouteReadOwnsNativeCatalog = errors.New("codex native catalog is owned by the route read of this invocation")

// codexResumeRouteInventory is the settled route span result. Both the rollout
// scan's opener and the native reads read it after the span terminalized, so
// the scan never has to wait for a route to start scanning.
type codexResumeRouteInventory struct {
	routes []NativeCodexRoute
	err    error
}

// codexResumeRolloutScan is the one rollout result of this invocation.
type codexResumeRolloutScan struct {
	discovery aisessions.ResumeSummaryDiscovery
	err       error
}

// catalogRouteInventory resolves the valid generation routes on the route clock
// and names the one terminal reason when there are none.
func (c *ResumeDiscovery) catalogRouteInventory() codexResumeRouteInventory {
	routes, err := c.catalogRoutesWithinBudget()
	valid := make([]NativeCodexRoute, 0, len(routes))
	for _, route := range routes {
		if route.Valid() {
			valid = append(valid, route)
		}
	}
	if err == nil && len(valid) == 0 {
		// An unavailable pool source may still yield rollout rows, but it must
		// not reopen the ambient/default native catalog and accidentally grant
		// native authority. This reason keeps that fallback explicitly
		// source-only.
		err = &NativeCodexRouteError{Reason: NativeCodexReasonGenerationUnavailable}
	}
	return codexResumeRouteInventory{routes: valid, err: err}
}

// resumeDiscoveryIsNativeAuthority reports whether a route read settled on the
// native catalog. Native-empty keeps authority, so a settled native source with
// zero rows is authority just as much as native rows are, and neither is
// replaced by the rollout scan of the same invocation.
func resumeDiscoveryIsNativeAuthority(discovery aisessions.ResumeSummaryDiscovery) bool {
	if discovery.Codex.Source == aisessions.CatalogSourceNative {
		return true
	}
	for _, summary := range discovery.Summaries {
		if summary.Source == aisessions.SourceCodexAppServer {
			return true
		}
	}
	return false
}

func (c *ResumeDiscovery) discoverResumeProvider(
	ctx context.Context,
	discover func(context.Context, string, string, aisessions.ResumeSummaryOptions, int) (aisessions.ResumeSummaryDiscovery, error),
	provider string,
) (aisessions.ResumeSummaryDiscovery, []NativeCodexRoute, error) {
	options := func(open aisessions.OpenCodexCatalog) aisessions.ResumeSummaryOptions {
		return aisessions.ResumeSummaryOptions{DiscoverOptions: aisessions.DiscoverOptions{
			HomeDir: c.home, Depth: c.depth, DeferTurns: true, OpenCodexCatalog: open,
		}, NativeBudget: c.budgets.Stage(ResumeStageNative)}
	}
	if provider != aiModeCodex || c.cmd.codexNative == nil {
		discovery, err := discover(ctx, provider, c.cwd, options(c.cmd.openCodexCatalog), c.limit)
		return discovery, nil, err
	}

	// The rollout scan starts with the provider, not after the route is known.
	// The route has to spend its own bound before it can name an endpoint, so a
	// scan entered at route_end settles past the declared provider terminal even
	// when it already holds rows this invocation could publish. Running it
	// alongside the route is what makes route + native + handoff the real worst
	// case. Its span is a sibling of the route span, so neither cancellation
	// reaches the other or any other provider.
	fallbackSpan := c.budgets.Start(c.ctx, ResumeStageFallback)
	defer fallbackSpan.Stop()

	var inventory codexResumeRouteInventory
	inventoryReady := make(chan struct{})
	go func() {
		defer close(inventoryReady)
		inventory = c.catalogRouteInventory()
	}()

	// One scan per invocation, entered here and not once per route. Its own
	// native attempt exists only to carry the route span's terminal reason into
	// the rollout result; it never opens an endpoint, so it cannot grant native
	// authority or race the route reads for the same connection.
	scanned := make(chan codexResumeRolloutScan, 1)
	go func() {
		discovery, err := discover(fallbackSpan.Ctx, provider, c.cwd, options(func(context.Context) (aisessions.CodexCatalog, error) {
			select {
			case <-inventoryReady:
			case <-c.ctx.Done():
				return nil, c.ctx.Err()
			}
			if inventory.err != nil {
				return nil, inventory.err
			}
			return nil, errCodexRouteReadOwnsNativeCatalog
		}), c.limit)
		scanned <- codexResumeRolloutScan{discovery: discovery, err: err}
	}()

	<-inventoryReady
	if inventory.err != nil {
		scan := <-scanned
		return scan.discovery, nil, scan.err
	}

	// The native reads start now, with the route resolved, and each one owns the
	// native bound from its own start instant. The rollout scan of this
	// invocation is already running, so these reads declare no rollout budget:
	// entering a second scan here is what used to push a route-slow invocation
	// past its terminal.
	routeOptions := func(open aisessions.OpenCodexCatalog) aisessions.ResumeSummaryOptions {
		opts := options(open)
		opts.FallbackBudget = ResumeCodexRouteReadRolloutBudget
		return opts
	}
	var combined aisessions.ResumeSummaryDiscovery
	var failures []error
	nativeSettled := false
	resolvedRoutes := make([]NativeCodexRoute, 0, len(inventory.routes))
	openRoute := c.cmd.openRoute
	if openRoute == nil {
		openRoute = OpenNativeCodexRoute
	}
	for _, route := range inventory.routes {
		resolvedRoutes = append(resolvedRoutes, route)
		generation, err := discover(fallbackSpan.Ctx, provider, c.cwd, routeOptions(func(openCtx context.Context) (aisessions.CodexCatalog, error) {
			client, openErr := openRoute(openCtx, route, false)
			if openErr != nil {
				return nil, openErr
			}
			return aisessions.NewCodexCatalog(client), nil
		}), c.limit)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if !resumeDiscoveryIsNativeAuthority(generation) {
			// This route's native read did not settle on the native catalog. Its
			// rows are not this invocation's rollout source either: that source is
			// the single scan above, so nothing from this read is carried forward.
			continue
		}
		nativeSettled = true
		for i := range generation.Summaries {
			if generation.Summaries[i].Source != aisessions.SourceCodexAppServer {
				continue
			}
			generation.Summaries[i].StateDomainID = route.Endpoint.StateDomainID
			generation.Summaries[i].EndpointGenerationID = route.Endpoint.EndpointGenerationID
			generation.Summaries[i].GenerationState = string(route.State)
		}
		for i := range generation.DetailRefs {
			if generation.DetailRefs[i].Source != aisessions.SourceCodexAppServer {
				continue
			}
			generation.DetailRefs[i].StateDomainID = route.Endpoint.StateDomainID
			generation.DetailRefs[i].EndpointGenerationID = route.Endpoint.EndpointGenerationID
			generation.DetailRefs[i].GenerationState = string(route.State)
		}
		combined.Summaries = append(combined.Summaries, generation.Summaries...)
		combined.DetailRefs = append(combined.DetailRefs, generation.DetailRefs...)
		combined.Codex = generation.Codex
		combined.MoreNotLoaded = combined.MoreNotLoaded || generation.MoreNotLoaded
	}
	if nativeSettled {
		return c.resolveCodexCatalogCollisions(combined), resolvedRoutes, nil
	}
	// No route read settled on the native catalog, whether it declined
	// authority or failed outright. Either way the rollout scan that has been
	// running since the provider started is the source of this invocation: it
	// is already settled or inside its own bound here, and it is never started
	// at this point.
	scan := <-scanned
	if len(scan.discovery.Summaries) == 0 && len(failures) > 0 && len(failures) == len(inventory.routes) {
		// Every route read failed and the scan found nothing, so the joined
		// route failures are the one terminal reason and there is no inventory
		// to publish.
		return combined, nil, errors.Join(failures...)
	}
	return scan.discovery, resolvedRoutes, scan.err
}

// OpenNativeCodexRoute connects to one native Codex generation: the default
// endpoint, or the private socket the route names.
func OpenNativeCodexRoute(ctx context.Context, route NativeCodexRoute, experimental bool) (*codexappserver.Client, error) {
	if route.Default {
		client, _, err := codexappserver.AttachDefaultEndpoint(ctx, version.String(), codexappserver.AttachOptions{
			Timeout: nativeCodexTimeout, ExperimentalAPI: experimental,
		})
		return client, err
	}
	return codexappserver.OpenPrivateUnix(ctx, route.SocketPath, nativeCodexTimeout, version.String(), experimental)
}

func codexCatalogRouteKey(endpoint coremetadata.CodexEndpointRef) string {
	return endpoint.StateDomainID + "\x00" + endpoint.EndpointGenerationID
}

// CodexSummaryEndpoint is the native endpoint a resume row was read from.
func CodexSummaryEndpoint(summary aisessions.ResumeSummary) coremetadata.CodexEndpointRef {
	return coremetadata.CodexEndpointRef{StateDomainID: strings.TrimSpace(summary.StateDomainID), EndpointGenerationID: strings.TrimSpace(summary.EndpointGenerationID)}
}

func codexDetailEndpoint(ref aisessions.ResumeDetailRef) coremetadata.CodexEndpointRef {
	return coremetadata.CodexEndpointRef{StateDomainID: strings.TrimSpace(ref.StateDomainID), EndpointGenerationID: strings.TrimSpace(ref.EndpointGenerationID)}
}

// resolveCodexCatalogCollisions closes the cross-generation ownership rule at
// the list boundary. A thread visible from multiple app-servers is assigned to
// a generation only when a durable Agent ref names that exact owner. Without
// one, the deterministic row is marked blocked, which keeps it searchable but
// makes selection generation-unavailable before any provider or Registry write.
func (c *ResumeDiscovery) resolveCodexCatalogCollisions(discovery aisessions.ResumeSummaryDiscovery) aisessions.ResumeSummaryDiscovery {
	groups := make(map[string][]aisessions.ResumeSummary)
	for _, summary := range discovery.Summaries {
		key := normalizeAIMode(summary.Provider) + "\x00" + strings.TrimSpace(summary.ResumeID)
		groups[key] = append(groups[key], summary)
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	selected := make([]aisessions.ResumeSummary, 0, len(groups))
	detailRefs := make([]aisessions.ResumeDetailRef, 0, len(groups))
	for _, key := range keys {
		group := groups[key]
		native := make(map[string][]aisessions.ResumeSummary)
		for _, summary := range group {
			endpoint := CodexSummaryEndpoint(summary)
			if summary.Source == aisessions.SourceCodexAppServer && endpoint.Valid() {
				native[codexCatalogRouteKey(endpoint)] = append(native[codexCatalogRouteKey(endpoint)], summary)
			}
		}
		chosen := newestResumeSummary(group)
		ambiguous := false
		if len(native) != 0 {
			owner, ownerKnown := c.knownCodexThreadOwner(chosen.ResumeID)
			if rows := native[codexCatalogRouteKey(owner)]; ownerKnown && len(rows) != 0 {
				chosen = newestResumeSummary(rows)
			} else if ownerKnown {
				nativeKeys := make([]string, 0, len(native))
				for nativeKey := range native {
					nativeKeys = append(nativeKeys, nativeKey)
				}
				sort.Strings(nativeKeys)
				chosen = newestResumeSummary(native[nativeKeys[0]])
				chosen.GenerationState = string(coremetadata.CodexGenerationBlocked)
				ambiguous = true
			} else if len(native) == 1 {
				for _, rows := range native {
					chosen = newestResumeSummary(rows)
				}
			} else {
				nativeKeys := make([]string, 0, len(native))
				for nativeKey := range native {
					nativeKeys = append(nativeKeys, nativeKey)
				}
				sort.Strings(nativeKeys)
				chosen = newestResumeSummary(native[nativeKeys[0]])
				chosen.GenerationState = string(coremetadata.CodexGenerationBlocked)
				ambiguous = true
			}
		}
		selected = append(selected, chosen)
		if ambiguous {
			continue
		}
		chosenEndpoint := CodexSummaryEndpoint(chosen)
		var matching []aisessions.ResumeDetailRef
		for _, ref := range discovery.DetailRefs {
			if normalizeAIMode(ref.Provider)+"\x00"+strings.TrimSpace(ref.ResumeID) != key || ref.Source != chosen.Source {
				continue
			}
			if chosen.Source == aisessions.SourceCodexAppServer && !codexDetailEndpoint(ref).Same(chosenEndpoint) {
				continue
			}
			matching = append(matching, ref)
		}
		if len(matching) != 0 {
			detailRefs = append(detailRefs, newestResumeDetailRef(matching))
		}
	}
	discovery.Summaries, discovery.DetailRefs = selected, detailRefs
	return discovery
}

func newestResumeSummary(rows []aisessions.ResumeSummary) aisessions.ResumeSummary {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].LastModified.Equal(rows[j].LastModified) {
			return codexCatalogRouteKey(CodexSummaryEndpoint(rows[i])) < codexCatalogRouteKey(CodexSummaryEndpoint(rows[j]))
		}
		return rows[i].LastModified.After(rows[j].LastModified)
	})
	return rows[0]
}

func newestResumeDetailRef(rows []aisessions.ResumeDetailRef) aisessions.ResumeDetailRef {
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].LastModified.After(rows[j].LastModified) })
	return rows[0]
}

func (c *ResumeDiscovery) knownCodexThreadOwner(threadID string) (coremetadata.CodexEndpointRef, bool) {
	if c.cmd.loadRegistry == nil {
		return coremetadata.CodexEndpointRef{}, false
	}
	registry, err := c.cmd.loadRegistry()
	if err != nil {
		return coremetadata.CodexEndpointRef{}, false
	}
	var owner coremetadata.CodexEndpointRef
	for _, agent := range registry.Agents {
		ref := agent.Status.SessionRef
		if agent.Spec.Provider != aiModeCodex || ref == nil || ref.Codex == nil ||
			strings.TrimSpace(ref.Codex.ThreadID) != strings.TrimSpace(threadID) || ref.Codex.Endpoint == nil || !ref.Codex.Endpoint.Valid() {
			continue
		}
		if owner.Valid() && !owner.Same(*ref.Codex.Endpoint) {
			return coremetadata.CodexEndpointRef{}, false
		}
		owner = *ref.Codex.Endpoint
	}
	return owner, owner.Valid()
}

// ResumeProviderOutcome is the settled state of one provider result and the
// rows it contributes, the projection a picker footer and a candidate list
// both read.
func ResumeProviderOutcome(result ResumeProviderResult, enabled bool) (ResumeProviderState, int) {
	if !enabled {
		return ResumeProviderDisabled, 0
	}
	// A Claude count is this invocation's parsed, confirmed unique matching
	// rows, including its terminal mtime tie group, before the global cap.
	// An early stop means this is not the accumulated transcript total.
	count := 0
	for _, summary := range result.Discovery.Summaries {
		if normalizeAIMode(summary.Provider) == normalizeAIMode(result.Provider) {
			count++
		}
	}
	// A result that carries rows is a found result, whatever the transport had
	// to fall back through to produce them: rollout rows this invocation
	// already holds are not a search failure. Empty results distinguish a real
	// provider error from an envelope that expired before any row was confirmed.
	if count == 0 && result.Err != nil {
		return ResumeProviderSearchFailed, 0
	}
	if count == 0 && result.EnvelopeExpired {
		return ResumeProviderScanUnfinished, 0
	}
	return ResumeProviderCount, count
}

// SettleResumeSummaries is the sole global dedupe/sort/cap pass.
func SettleResumeSummaries(summaries []aisessions.ResumeSummary, limit int) ([]aisessions.ResumeSummary, bool) {
	byKey := make(map[string]aisessions.ResumeSummary, len(summaries))
	for _, summary := range summaries {
		key := normalizeAIMode(summary.Provider) + "\x00" + strings.TrimSpace(summary.ResumeID)
		if old, ok := byKey[key]; !ok || summary.LastModified.After(old.LastModified) {
			byKey[key] = summary
		}
	}
	result := make([]aisessions.ResumeSummary, 0, len(byKey))
	for _, summary := range byKey {
		result = append(result, summary)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].LastModified.Equal(result[j].LastModified) {
			if result[i].Provider == result[j].Provider {
				return result[i].ResumeID < result[j].ResumeID
			}
			return result[i].Provider < result[j].Provider
		}
		return result[i].LastModified.After(result[j].LastModified)
	})
	limit = NormalizeResumePickerLimit(limit)
	capped := len(result) > limit
	if capped {
		result = result[:limit]
	}
	return result, capped
}
