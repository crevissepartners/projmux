package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"
	"sync"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

const (
	aiResumePreviewTimeout          = 2 * time.Second
	aiResumeSummaryPopulationBudget = 450 * time.Millisecond
)

type aiResumePreviewKey struct {
	provider  string
	id        string
	updatedAt time.Time
}

type aiResumeLiveController struct {
	cmd               *aiCommand
	ctx               context.Context
	cancel            context.CancelFunc
	cwd               string
	depth             int
	limit             int
	home              string
	locale            i18n.Locale
	now               time.Time
	previewTimeout    time.Duration
	populationTimeout time.Duration
	populationContext func(context.Context, time.Duration) (context.Context, context.CancelFunc)
	budgets           aiResumeBudgets
	events            chan struct{}
	startOnce         sync.Once
	labelsOnce        sync.Once
	labels            map[string]aiResumeExactAgentLabel

	mu              sync.Mutex
	summaries       []aisessions.ResumeSummary
	detailRefs      map[string]aisessions.ResumeDetailRef
	providerStates  map[string]aiResumeProviderProjection
	providerEnabled map[string]bool
	// providerElapsed is the settlement witness: one duration per provider,
	// each measured from that provider's own start instant to that provider's
	// own terminal on the budget clock. C-4 keeps it out of every user-visible
	// surface, so no row, value, footer string, or Registry write reads it. It
	// exists only so provider-independent settlement is observable at all.
	providerElapsed map[string]time.Duration
	detailText      map[string]string
	detailCache     map[aiResumePreviewKey]string
	detailReads     map[aiResumePreviewKey]bool
	catalogRoutes   map[string]codexNativeEndpointRoute
	previewCancel   context.CancelFunc
	focusedValue    string
	moreNotLoaded   bool
}

func newAIResumeLiveController(cmd *aiCommand, cwd, home string, depth, limit int) *aiResumeLiveController {
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Time{}
	if cmd.now != nil {
		now = cmd.now()
	}
	enabled := make(map[string]bool, 3)
	for _, provider := range cmd.enabledAIAgents() {
		enabled[normalizeAIMode(string(provider))] = true
	}
	return &aiResumeLiveController{
		cmd: cmd, ctx: ctx, cancel: cancel, cwd: cwd, home: home, depth: depth,
		limit: normalizeResumePickerLimit(limit), locale: appLocale(cmd.homeDir, cmd.lookupEnv), now: now,
		previewTimeout: aiResumePreviewTimeout, populationTimeout: aiResumeSummaryPopulationBudget,
		populationContext: context.WithTimeout, budgets: defaultAIResumeBudgets(),
		events: make(chan struct{}, 16), providerStates: map[string]aiResumeProviderProjection{}, providerEnabled: enabled,
		providerElapsed: map[string]time.Duration{},
		detailRefs:      map[string]aisessions.ResumeDetailRef{}, detailText: map[string]string{},
		detailCache: map[aiResumePreviewKey]string{}, detailReads: map[aiResumePreviewKey]bool{},
		catalogRoutes: map[string]codexNativeEndpointRoute{},
	}
}

func (c *aiResumeLiveController) close() {
	c.cancel()
	c.mu.Lock()
	if c.previewCancel != nil {
		c.previewCancel()
	}
	c.mu.Unlock()
}

func (c *aiResumeLiveController) signal() {
	if c.ctx.Err() != nil {
		return
	}
	select {
	case c.events <- struct{}{}:
	default:
	}
}

type aiResumeProviderResult struct {
	provider        string
	discovery       aisessions.ResumeSummaryDiscovery
	routes          []codexNativeEndpointRoute
	err             error
	envelopeExpired bool
	// elapsed is this provider's own witness, measured from this provider's
	// start instant. It is never derived from the frame's start or from a
	// sibling's span, and it never leaves this settlement.
	elapsed time.Duration
}

type aiResumeProviderState string

const (
	aiResumeProviderCount        aiResumeProviderState = "count"
	aiResumeProviderSearchFailed aiResumeProviderState = "search_failed"
	aiResumeProviderDisabled     aiResumeProviderState = "disabled"
)

type aiResumeProviderProjection struct {
	state aiResumeProviderState
	count int
}

// populate settles all provider summaries before the picker is opened. The
// caller sees one final row set: provider completion order and late returns can
// never append, reorder, or replace Items in the current invocation.
func (c *aiResumeLiveController) populate() {
	c.startOnce.Do(c.populateOnce)
}

func (c *aiResumeLiveController) populateOnce() {
	timeout := c.populationTimeout
	if timeout <= 0 {
		timeout = aiResumeSummaryPopulationBudget
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
	results := make(chan aiResumeProviderResult, len(providers))
	settled := make(map[string]aiResumeProviderResult, len(providers))
	pending := 0
	for _, provider := range providers {
		if !c.providerEnabled[provider] {
			settled[provider] = aiResumeProviderResult{provider: provider}
			continue
		}
		// Every enabled provider opens its own envelope here, at its own start
		// instant, with its own declared bound. There is no shared discovery
		// deadline left to inherit, so one provider being slow, failing, or not
		// being configured at all cannot move where a sibling terminalizes.
		envelope, stop := withTimeout(c.ctx, c.providerEnvelope(provider, timeout))
		startedAt := c.budgets.instant()
		defer stop()
		pending++
		go func() {
			returned := make(chan aiResumeProviderResult, 1)
			go func() {
				discovery, routes, err := c.discoverResumeProvider(envelope, discover, provider)
				returned <- aiResumeProviderResult{provider: provider, discovery: discovery, routes: routes, err: err}
			}()
			result := c.settleProviderResult(provider, envelope, startedAt, returned)
			c.recordProviderElapsed(provider, result.elapsed)
			results <- result
		}()
	}

	// Each goroutine above terminalizes on its own clock and then sends exactly
	// one result, so collecting them needs no envelope of its own: the frame is
	// the set of provider terminals, not a deadline that overrides them.
	for range pending {
		result := <-results
		settled[result.provider] = result
	}

	var summaries []aisessions.ResumeSummary
	var routes []codexNativeEndpointRoute
	allDetailRefs := make(map[string]aisessions.ResumeDetailRef)
	providerStates := make(map[string]aiResumeProviderProjection, len(providers))
	moreNotLoaded := false
	for _, provider := range providers {
		result := settled[provider]
		projection := resumeProviderProjection(result, c.providerEnabled[provider])
		providerStates[provider] = projection
		// The footer count and the rows published for a provider come from the
		// same settled result, so a provider can never report rows it did not
		// contribute or contribute rows it did not report.
		if projection.state == aiResumeProviderCount {
			summaries = append(summaries, result.discovery.Summaries...)
			routes = append(routes, result.routes...)
			for _, ref := range result.discovery.DetailRefs {
				key := normalizeAIMode(ref.Provider) + "\x00" + strings.TrimSpace(ref.ResumeID)
				if old, ok := allDetailRefs[key]; !ok || ref.LastModified.After(old.LastModified) {
					allDetailRefs[key] = ref
				}
			}
			moreNotLoaded = moreNotLoaded || result.discovery.MoreNotLoaded
		}
	}
	// One global dedupe/sort/cap pass over the settled rows of all three
	// providers. The provider counts above are already fixed and stay pre-cap.
	summaries, capped := settleAIResumeSummaries(summaries, c.limit)
	moreNotLoaded = moreNotLoaded || capped
	c.mu.Lock()
	if c.ctx.Err() == nil {
		c.summaries = summaries
		c.detailRefs = make(map[string]aisessions.ResumeDetailRef, len(summaries))
		for _, summary := range summaries {
			key := normalizeAIMode(summary.Provider) + "\x00" + strings.TrimSpace(summary.ResumeID)
			if ref, ok := allDetailRefs[key]; ok {
				c.detailRefs[key] = ref
			}
		}
		// Route inventory is published with the frame it belongs to. A route
		// that resolves after this invocation settled is discarded instead of
		// mutating the preview lane behind an already immutable row set.
		for _, route := range routes {
			c.catalogRoutes[codexCatalogRouteKey(route.Endpoint)] = route
		}
		c.providerStates = providerStates
		c.moreNotLoaded = moreNotLoaded
	}
	c.mu.Unlock()
}

// providerEnvelope is one provider's own declared bound. Codex resolves routes
// and a bounded native page on its own staged clocks, so its envelope is the
// declared provider terminal; every other provider keeps the population
// budget. The bound belongs to the provider: whether Codex is configured, and
// how long it takes, never changes what Claude or Antigravity are given.
func (c *aiResumeLiveController) providerEnvelope(provider string, timeout time.Duration) time.Duration {
	if provider == aiModeCodex && c.codexBudgetStagesActive() {
		return max(timeout, c.budgets.providerTerminal())
	}
	return timeout
}

// settleProviderResult terminalizes one provider on that provider's own clock.
// The envelope bound and the handoff window that follows it are both measured
// from this provider's start instant, so a sibling still resolving neither
// expires this result nor extends it.
func (c *aiResumeLiveController) settleProviderResult(
	provider string,
	envelope context.Context,
	startedAt time.Time,
	returned <-chan aiResumeProviderResult,
) aiResumeProviderResult {
	select {
	case result := <-returned:
		result.elapsed = c.providerElapsedSince(startedAt)
		return result
	case <-envelope.Done():
	}
	// Provider cancellation and its buffered result send are separate scheduler
	// events. Reserve the declared handoff window after this provider's own
	// envelope so a cancellation-settled partial is not replaced by
	// available-empty solely because its send lost the select race.
	handoff := time.NewTimer(c.budgets.stage(aiResumeStageHandoff))
	defer handoff.Stop()
	select {
	case result := <-returned:
		result.envelopeExpired = errors.Is(result.err, context.Canceled) || errors.Is(result.err, context.DeadlineExceeded)
		result.elapsed = c.providerElapsedSince(startedAt)
		return result
	case <-handoff.C:
		return aiResumeProviderResult{provider: provider, envelopeExpired: true, elapsed: c.providerElapsedSince(startedAt)}
	}
}

// recordProviderElapsed publishes one provider's settlement witness at that
// provider's own terminal rather than at the frame's, which is what makes the
// value a provider clock instead of a frame clock. C-4 keeps it internal: it
// is not a row, a value, a footer string, or a Registry write, and nothing
// user-visible reads it.
func (c *aiResumeLiveController) recordProviderElapsed(provider string, elapsed time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.providerElapsed[provider] = elapsed
}

// providerElapsedSince reads the budget clock for one provider witness. The
// instant it subtracts is that provider's own start, never the frame's and
// never a sibling stage's.
func (c *aiResumeLiveController) providerElapsedSince(startedAt time.Time) time.Duration {
	elapsed := c.budgets.instant().Sub(startedAt)
	if elapsed < 0 {
		return 0
	}
	return elapsed
}

// codexBudgetStagesActive reports whether this invocation resolves Codex
// routes, which is the only path that owns the staged budgets.
func (c *aiResumeLiveController) codexBudgetStagesActive() bool {
	return c.providerEnabled[aiModeCodex] && c.cmd != nil && c.cmd.codexNative != nil
}

// catalogRoutesWithinBudget resolves the generation inventory on the route
// clock alone, started under the invocation lifetime rather than under the
// provider discovery envelope. CatalogRoutes is a read-only projection, so a
// return that arrives after the route bound is discarded: the span has already
// produced the one terminal reason for this invocation.
func (c *aiResumeLiveController) catalogRoutesWithinBudget() ([]codexNativeEndpointRoute, error) {
	span := c.budgets.start(c.ctx, aiResumeStageRoute)
	defer span.stop()
	type routeInventory struct {
		routes []codexNativeEndpointRoute
		err    error
	}
	// Buffered so a late CatalogRoutes return neither blocks its goroutine nor
	// reaches a settled invocation.
	resolved := make(chan routeInventory, 1)
	go func() {
		routes, err := c.cmd.codexNative.CatalogRoutes(span.ctx)
		resolved <- routeInventory{routes: routes, err: err}
	}()
	select {
	case inventory := <-resolved:
		return inventory.routes, inventory.err
	case <-span.ctx.Done():
		// Cause separates "this stage spent its own bound" from "the invocation
		// was closed"; only the former is a route budget timeout.
		if errors.Is(context.Cause(span.ctx), context.DeadlineExceeded) {
			return nil, c.budgets.timeout(span)
		}
		return nil, span.ctx.Err()
	}
}

// codexRouteReadRolloutBudget is the rollout bound a per-route native read
// declares for itself. This invocation enters the rollout scan exactly once,
// with the provider, under fallbackSpan. A route read that opened a scan of its
// own would enter a second one and start that one at route_end: the very
// start instant this stage exists to remove, and a bound the declared provider
// terminal does not include. ResumeSummaryOptions spells "no scan of my own" as
// an already spent positive bound, because a zero value selects the declared
// default instead.
const codexRouteReadRolloutBudget = time.Nanosecond

// errCodexRouteReadOwnsNativeCatalog refuses a native endpoint to the rollout
// scan's own catalog attempt. The scan is source-only: the endpoint named by a
// resolved route belongs to the route reads, which start when the route has
// actually resolved and own the native bound from that instant.
var errCodexRouteReadOwnsNativeCatalog = errors.New("codex native catalog is owned by the route read of this invocation")

// codexResumeRouteInventory is the settled route span result. Both the rollout
// scan's opener and the native reads read it after the span terminalized, so
// the scan never has to wait for a route to start scanning.
type codexResumeRouteInventory struct {
	routes []codexNativeEndpointRoute
	err    error
}

// codexResumeRolloutScan is the one rollout result of this invocation.
type codexResumeRolloutScan struct {
	discovery aisessions.ResumeSummaryDiscovery
	err       error
}

// catalogRouteInventory resolves the valid generation routes on the route clock
// and names the one terminal reason when there are none.
func (c *aiResumeLiveController) catalogRouteInventory() codexResumeRouteInventory {
	routes, err := c.catalogRoutesWithinBudget()
	valid := make([]codexNativeEndpointRoute, 0, len(routes))
	for _, route := range routes {
		if route.valid() {
			valid = append(valid, route)
		}
	}
	if err == nil && len(valid) == 0 {
		// An unavailable pool source may still yield rollout rows, but it must
		// not reopen the ambient/default native catalog and accidentally grant
		// native authority. This reason keeps that fallback explicitly
		// source-only.
		err = &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable}
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

func (c *aiResumeLiveController) discoverResumeProvider(
	ctx context.Context,
	discover func(context.Context, string, string, aisessions.ResumeSummaryOptions, int) (aisessions.ResumeSummaryDiscovery, error),
	provider string,
) (aisessions.ResumeSummaryDiscovery, []codexNativeEndpointRoute, error) {
	options := func(open aisessions.OpenCodexCatalog) aisessions.ResumeSummaryOptions {
		return aisessions.ResumeSummaryOptions{DiscoverOptions: aisessions.DiscoverOptions{
			HomeDir: c.home, Depth: c.depth, DeferTurns: true, OpenCodexCatalog: open,
		}, NativeBudget: c.budgets.stage(aiResumeStageNative)}
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
	fallbackSpan := c.budgets.start(c.ctx, aiResumeStageFallback)
	defer fallbackSpan.stop()

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
		discovery, err := discover(fallbackSpan.ctx, provider, c.cwd, options(func(context.Context) (aisessions.CodexCatalog, error) {
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
		opts.FallbackBudget = codexRouteReadRolloutBudget
		return opts
	}
	var combined aisessions.ResumeSummaryDiscovery
	var failures []error
	nativeSettled := false
	resolvedRoutes := make([]codexNativeEndpointRoute, 0, len(inventory.routes))
	for _, route := range inventory.routes {
		resolvedRoutes = append(resolvedRoutes, route)
		generation, err := discover(fallbackSpan.ctx, provider, c.cwd, routeOptions(func(openCtx context.Context) (aisessions.CodexCatalog, error) {
			client, openErr := openCodexNativeRoute(openCtx, route, false)
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

func codexCatalogRouteKey(endpoint coremetadata.CodexEndpointRef) string {
	return endpoint.StateDomainID + "\x00" + endpoint.EndpointGenerationID
}

func codexSummaryEndpoint(summary aisessions.ResumeSummary) coremetadata.CodexEndpointRef {
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
func (c *aiResumeLiveController) resolveCodexCatalogCollisions(discovery aisessions.ResumeSummaryDiscovery) aisessions.ResumeSummaryDiscovery {
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
			endpoint := codexSummaryEndpoint(summary)
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
		chosenEndpoint := codexSummaryEndpoint(chosen)
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
			return codexCatalogRouteKey(codexSummaryEndpoint(rows[i])) < codexCatalogRouteKey(codexSummaryEndpoint(rows[j]))
		}
		return rows[i].LastModified.After(rows[j].LastModified)
	})
	return rows[0]
}

func newestResumeDetailRef(rows []aisessions.ResumeDetailRef) aisessions.ResumeDetailRef {
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].LastModified.After(rows[j].LastModified) })
	return rows[0]
}

func (c *aiResumeLiveController) knownCodexThreadOwner(threadID string) (coremetadata.CodexEndpointRef, bool) {
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

func resumeProviderProjection(result aiResumeProviderResult, enabled bool) aiResumeProviderProjection {
	if !enabled {
		return aiResumeProviderProjection{state: aiResumeProviderDisabled}
	}
	count := 0
	for _, summary := range result.discovery.Summaries {
		if normalizeAIMode(summary.Provider) == normalizeAIMode(result.provider) {
			count++
		}
	}
	// A result that carries rows is a found result, whatever the transport had
	// to fall back through to produce them: rollout rows this invocation
	// already holds are not a search failure. search_failed stays reserved for
	// a provider that reached its terminal with nothing to show.
	if count == 0 && (result.err != nil || result.envelopeExpired) {
		return aiResumeProviderProjection{state: aiResumeProviderSearchFailed}
	}
	return aiResumeProviderProjection{state: aiResumeProviderCount, count: count}
}

// settleAIResumeSummaries is the sole global dedupe/sort/cap pass.
func settleAIResumeSummaries(summaries []aisessions.ResumeSummary, limit int) ([]aisessions.ResumeSummary, bool) {
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
	limit = normalizeResumePickerLimit(limit)
	capped := len(result) > limit
	if capped {
		result = result[:limit]
	}
	return result, capped
}

func (c *aiResumeLiveController) initialEntries() []intpickercompat.Entry {
	c.populate()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.entries(c.summaries)
}

func (c *aiResumeLiveController) entries(summaries []aisessions.ResumeSummary) []intpickercompat.Entry {
	var hasProvider bool
	for _, summary := range summaries {
		if _, ok := aiModeProvider(summary.Provider); ok {
			hasProvider = true
			break
		}
	}
	if hasProvider {
		c.labelsOnce.Do(func() { c.labels = c.cmd.resolveAIResumeSummaryLabels(summaries) })
	}
	labels := c.labels
	return aiResumeSummaryRowsWithLabels(summaries, labels, c.now, c.locale, c.cwd, c.depth)
}

func (c *aiResumeLiveController) footer() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return resumePickerFooter(c.providerStates, len(c.summaries), c.locale), c.moreNotLoaded
}

func resumePickerFooter(states map[string]aiResumeProviderProjection, visible int, locale i18n.Locale) string {
	providerLine := resumeProviderFooterLine(states, locale)
	shownCount := fmt.Sprintf(localizeUIText(locale, "Showing latest %d resume sessions."), visible)
	return projmuxFooter(providerLine + "\n" + shownCount)
}

func resumeProviderFooterLine(states map[string]aiResumeProviderProjection, locale i18n.Locale) string {
	providers := []struct {
		id    string
		label string
	}{
		{id: aiModeCodex, label: "Codex"},
		{id: aiModeClaude, label: "Claude"},
		{id: aiModeAntigravity, label: "Antigravity"},
	}
	parts := make([]string, 0, len(providers))
	for _, provider := range providers {
		projection := states[provider.id]
		if projection.state == "" {
			projection.state = aiResumeProviderCount
		}
		parts = append(parts, provider.label+" "+resumeProviderStateText(locale, projection))
	}
	return localizeUIText(locale, "Providers") + " " + strings.Join(parts, " · ")
}

func resumeProviderStateText(locale i18n.Locale, projection aiResumeProviderProjection) string {
	text, err := i18n.NewLocalizer(locale).Text(i18n.Key("picker.ai.resume_provider_" + string(projection.state)))
	if err != nil {
		return string(projection.state)
	}
	if projection.state == aiResumeProviderCount {
		return fmt.Sprintf(text.String(), projection.count)
	}
	return text.String()
}

func (c *aiResumeLiveController) update() (intpicker.DeferredUpdate, error) {
	c.mu.Lock()
	detail := make(map[string]string, len(c.detailText))
	maps.Copy(detail, c.detailText)
	c.mu.Unlock()
	return intpicker.DeferredUpdate{
		SelectionDetail: &intpicker.SelectionDetail{TextByValue: detail},
	}, nil
}

func (c *aiResumeLiveController) initialDetail() *intpicker.SelectionDetail {
	return &intpicker.SelectionDetail{TextByValue: map[string]string{
		aiResumeNewValue: localizeText(c.locale, i18n.KeyPickerResumeDetailHelp, "Select a resume session to see details."),
	}}
}

func (c *aiResumeLiveController) focus(value string) {
	c.mu.Lock()
	if value == c.focusedValue {
		c.mu.Unlock()
		return
	}
	c.focusedValue = value
	if c.previewCancel != nil {
		c.previewCancel()
		c.previewCancel = nil
	}
	c.detailText = map[string]string{}
	selection, ok := parseAIResumePickerValue(value)
	if !ok {
		if value == aiResumeNewValue {
			c.detailText[value] = localizeText(c.locale, i18n.KeyPickerResumeDetailHelp, "Select a resume session to see details.")
		}
		c.mu.Unlock()
		c.signal()
		return
	}
	var summary aisessions.ResumeSummary
	var detailRef aisessions.ResumeDetailRef
	found := false
	for _, candidate := range c.summaries {
		if aiResumeSummaryMatchesSelection(candidate, selection) {
			summary, found = candidate, true
			detailRef = c.detailRefs[normalizeAIMode(candidate.Provider)+"\x00"+strings.TrimSpace(candidate.ResumeID)]
			break
		}
	}
	if !found {
		c.mu.Unlock()
		c.signal()
		return
	}
	cacheKey := aiResumePreviewCacheKey(summary)
	if cached, hit := c.detailCache[cacheKey]; hit {
		c.detailText[value] = cached
		c.mu.Unlock()
		c.signal()
		return
	}
	binding := c.labels[aiResumeExactLabelKey(summary.Provider, summary.ResumeID)]
	loading := aiResumeDetailProjection(c.locale, summary, detailRef, aisessions.ResumeDetail{}, localizeUIText(c.locale, "Loading preview…"), binding)
	c.detailText[value] = loading
	if c.detailReads[cacheKey] {
		c.mu.Unlock()
		c.signal()
		return
	}
	c.detailReads[cacheKey] = true
	timeout := c.previewTimeout
	if timeout <= 0 {
		timeout = aiResumePreviewTimeout
	}
	previewCtx, cancel := context.WithTimeout(c.ctx, timeout)
	c.previewCancel = cancel
	previewCatalog := c.cmd.openCodexCatalog
	if summary.Source == aisessions.SourceCodexAppServer {
		previewCatalog = func(context.Context) (aisessions.CodexCatalog, error) {
			return nil, &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable}
		}
		detailEndpoint := codexDetailEndpoint(detailRef)
		if route, ok := c.catalogRoutes[codexCatalogRouteKey(detailEndpoint)]; ok && detailEndpoint.Valid() &&
			route.Endpoint.Same(detailEndpoint) && string(route.State) == strings.TrimSpace(detailRef.GenerationState) {
			route := route
			previewCatalog = func(openCtx context.Context) (aisessions.CodexCatalog, error) {
				client, openErr := openCodexNativeRoute(openCtx, route, false)
				if openErr != nil {
					return nil, openErr
				}
				return aisessions.NewCodexCatalog(client), nil
			}
		}
	}
	c.mu.Unlock()
	c.signal()
	go func() {
		defer cancel()
		var detail aisessions.ResumeDetail
		var err error
		if c.cmd.readResumePreview != nil {
			detail.Preview, err = c.cmd.readResumePreview(previewCtx, detailRef, previewCatalog)
			detail.Source = detailRef.Source
			detail.Turns = detailRef.Turns
			detail.Confidence = detailRef.Confidence
			detail.Reason = detailRef.Reason
			detail.RuntimeStatus = detailRef.RuntimeStatus
		} else {
			readDetail := c.cmd.readResumeDetail
			if readDetail == nil {
				readDetail = aisessions.ReadResumeDetail
			}
			detail, err = readDetail(previewCtx, detailRef, previewCatalog)
		}
		previewText := aisessions.FormatPreview(detail.Preview)
		if err != nil || strings.TrimSpace(previewText) == "" {
			previewText = localizeUIText(c.locale, "preview unavailable")
		}
		text := aiResumeDetailProjection(c.locale, summary, detailRef, detail, previewText, binding)
		c.mu.Lock()
		defer c.mu.Unlock()
		c.detailCache[cacheKey] = text
		if c.ctx.Err() != nil {
			return
		}
		focusedSelection, focused := parseAIResumePickerValue(c.focusedValue)
		if !focused || normalizeAIMode(focusedSelection.agent) != cacheKey.provider || strings.TrimSpace(focusedSelection.resumeID) != cacheKey.id {
			return
		}
		var focusedKey aiResumePreviewKey
		for _, candidate := range c.summaries {
			if normalizeAIMode(candidate.Provider) == cacheKey.provider && strings.TrimSpace(candidate.ResumeID) == cacheKey.id {
				focusedKey = aiResumePreviewCacheKey(candidate)
				break
			}
		}
		if focusedKey != cacheKey {
			return
		}
		c.detailText = map[string]string{c.focusedValue: text}
		c.signal()
	}()
}

func aiResumeSummaryMatchesSelection(summary aisessions.ResumeSummary, selection aiResumeSelection) bool {
	if normalizeAIMode(summary.Provider) != normalizeAIMode(selection.agent) || strings.TrimSpace(summary.ResumeID) != selection.resumeID {
		return false
	}
	if !selection.rowPinned {
		return true
	}
	return strings.TrimSpace(summary.Source) == selection.source &&
		strings.TrimSpace(summary.StateDomainID) == selection.endpoint.StateDomainID &&
		strings.TrimSpace(summary.EndpointGenerationID) == selection.endpoint.EndpointGenerationID &&
		strings.TrimSpace(summary.GenerationState) == string(selection.state)
}

func aiResumeDetailProjection(locale i18n.Locale, summary aisessions.ResumeSummary, ref aisessions.ResumeDetailRef, detail aisessions.ResumeDetail, previewText string, bindings ...aiResumeExactAgentLabel) string {
	idLabel := localizeText(locale, i18n.Key("picker.ai.resume_detail_id"), "Conversation ID")
	sourceLabel := localizeUIText(locale, "Source")
	turnsLabel := localizeText(locale, i18n.KeyPickerResumeDetailTurns, "Turns")
	runtimeLabel := localizeUIText(locale, "Runtime")
	confidenceLabel := localizeText(locale, i18n.KeyPickerResumeDetailConfidence, "Confidence")
	reasonLabel := localizeText(locale, i18n.KeyPickerResumeDetailReason, "Reason")
	detailsLabel := localizeText(locale, i18n.Key("picker.ai.resume_detail_metadata"), "Details")
	unavailable := localizeUIText(locale, "unavailable")
	provider := strings.TrimSpace(summary.Provider)
	var binding aiResumeExactAgentLabel
	if len(bindings) > 0 {
		binding = bindings[0]
	}
	title := aiResumeDisplayLabelForSummary(summary, nil, locale)
	if len(bindings) > 0 {
		title = aiResumeDisplayLabel(aiResumeSessionMetaFromSummary(summary, ""), binding, locale)
	}
	lines := []string{provider + " · " + title}
	if fragment := aiResumeBindingFragment(binding, 0); fragment != "" {
		bindingLabel := localizeText(locale, i18n.Key("picker.ai.resume_detail_binding"), "projmux binding")
		lines = append(lines, bindingLabel+": "+fragment)
	}
	source := strings.TrimSpace(detail.Source)
	if source == "" {
		source = strings.TrimSpace(ref.Source)
	}
	if source == "" {
		source = unavailable
	}
	turns := detail.Turns
	if turns == 0 {
		turns = ref.Turns
	}
	identity := idLabel + ": " + strings.TrimSpace(summary.ResumeID) + " · " + sourceLabel + ": " + source
	lines = append(lines, identity, localizeUIText(locale, "Preview"), previewText)
	metadata := make([]string, 0, 3)
	if turns > 0 {
		metadata = append(metadata, fmt.Sprintf("%s: %d", turnsLabel, turns))
	} else {
		metadata = append(metadata, turnsLabel+": "+unavailable)
	}
	runtimeStatus := strings.TrimSpace(detail.RuntimeStatus)
	if runtimeStatus == "" {
		runtimeStatus = strings.TrimSpace(ref.RuntimeStatus)
	}
	if runtimeStatus != "" {
		metadata = append(metadata, runtimeLabel+": "+runtimeStatus)
	}
	lines = append(lines, "", detailsLabel, strings.Join(metadata, " · "))
	confidence := strings.TrimSpace(detail.Confidence)
	if confidence == "" {
		confidence = strings.TrimSpace(ref.Confidence)
	}
	reason := strings.TrimSpace(detail.Reason)
	if reason == "" {
		reason = strings.TrimSpace(ref.Reason)
	}
	explanation := make([]string, 0, 2)
	if confidence != "" {
		explanation = append(explanation, confidenceLabel+": "+confidence)
	}
	if reason != "" {
		explanation = append(explanation, reasonLabel+": "+reason)
	}
	if len(explanation) > 0 {
		lines = append(lines, strings.Join(explanation, " · "))
	}
	lines = append(lines, aiResumeBindingDetailOverflow(locale, binding)...)
	return strings.Join(lines, "\n")
}

// The native detail viewport scrolls vertically. Keep the canonical full
// binding line, and expose overflow names as readable continuation lines in
// Details without changing the picker-wide renderer or wrap policy.
func aiResumeBindingDetailOverflow(locale i18n.Locale, binding aiResumeExactAgentLabel) []string {
	const maxCells = 64 // includes the label; fits the 80-column detail viewport
	fragment := aiResumeBindingFragment(binding, 0)
	bindingLabel := localizeText(locale, i18n.Key("picker.ai.resume_detail_binding"), "projmux binding")
	if fragment == "" || i18n.TerminalCellWidth(bindingLabel+": "+fragment) <= maxCells {
		return nil
	}
	var lines []string
	for _, field := range []struct {
		key            i18n.Key
		fallback, name string
	}{
		{i18n.Key("picker.ai.resume_detail_agent_name"), "Agent name", binding.Name},
		{i18n.Key("picker.ai.resume_detail_pane_name"), "Pane name", binding.PaneName},
	} {
		name := strings.TrimSpace(field.name)
		prefix := localizeText(locale, field.key, field.fallback) + ": "
		indent := strings.Repeat(" ", i18n.TerminalCellWidth(prefix))
		for name != "" {
			chunk := i18n.TruncateTerminalCells(name, maxCells-i18n.TerminalCellWidth(prefix))
			lines = append(lines, prefix+chunk)
			name = strings.TrimPrefix(name, chunk)
			prefix = indent
		}
	}
	return lines
}

func aiResumePreviewCacheKey(summary aisessions.ResumeSummary) aiResumePreviewKey {
	updatedAt := summary.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = summary.LastModified
	}
	return aiResumePreviewKey{provider: normalizeAIMode(strings.ToLower(strings.TrimSpace(summary.Provider))), id: strings.TrimSpace(summary.ResumeID), updatedAt: updatedAt}
}

func (c *aiResumeLiveController) snapshotSummaries() []aisessions.ResumeSummary {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]aisessions.ResumeSummary(nil), c.summaries...)
}
