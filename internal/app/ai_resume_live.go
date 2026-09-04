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
		detailRefs: map[string]aisessions.ResumeDetailRef{}, detailText: map[string]string{},
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
	ctx, cancel := withTimeout(c.ctx, timeout)
	defer cancel()
	// Non-Codex providers keep the shared discovery envelope. The Codex
	// provider measures route, native, and rollout on its own declared clocks,
	// so the frame settles on the longest declared provider bound instead of
	// discarding a result that is still inside it.
	settleTimeout := timeout
	if c.codexBudgetStagesActive() {
		settleTimeout = max(timeout, c.budgets.providerTerminal())
	}
	settle, cancelSettle := withTimeout(c.ctx, settleTimeout)
	defer cancelSettle()

	discover := c.cmd.discoverResumeSummaryProvider
	if discover == nil {
		discover = aisessions.DiscoverResumeSummariesContext
	}
	providers := []string{aiModeCodex, aiModeClaude, aiModeAntigravity}
	results := make(chan aiResumeProviderResult, len(providers))
	settled := make(map[string]aiResumeProviderResult, len(providers))
	for _, provider := range providers {
		if !c.providerEnabled[provider] {
			settled[provider] = aiResumeProviderResult{provider: provider}
			continue
		}
		go func() {
			discovery, routes, err := c.discoverResumeProvider(ctx, discover, provider)
			results <- aiResumeProviderResult{provider: provider, discovery: discovery, routes: routes, err: err}
		}()
	}

	for len(settled) < len(providers) {
		select {
		case result := <-results:
			if settle.Err() != nil {
				result.envelopeExpired = errors.Is(result.err, context.Canceled) || errors.Is(result.err, context.DeadlineExceeded)
			}
			settled[result.provider] = result
		case <-settle.Done():
			// Provider cancellation and its buffered result send are separate
			// scheduler events. Reserve the declared handoff window after the
			// settlement envelope so a cancellation-settled partial is not replaced
			// by available-empty solely because its send lost the select race.
			settlementTimer := time.NewTimer(c.budgets.stage(aiResumeStageHandoff))
		settleResults:
			for len(settled) < len(providers) {
				select {
				case result := <-results:
					result.envelopeExpired = errors.Is(result.err, context.Canceled) || errors.Is(result.err, context.DeadlineExceeded)
					settled[result.provider] = result
				case <-settlementTimer.C:
					break settleResults
				}
			}
			if !settlementTimer.Stop() {
				select {
				case <-settlementTimer.C:
				default:
				}
			}
			for _, provider := range providers {
				if _, ok := settled[provider]; !ok {
					settled[provider] = aiResumeProviderResult{provider: provider, envelopeExpired: true}
				}
			}
		}
	}

	var summaries []aisessions.ResumeSummary
	var routes []codexNativeEndpointRoute
	allDetailRefs := make(map[string]aisessions.ResumeDetailRef)
	providerStates := make(map[string]aiResumeProviderProjection, len(providers))
	moreNotLoaded := false
	for _, provider := range providers {
		result := settled[provider]
		providerStates[provider] = resumeProviderProjection(result, c.providerEnabled[provider])
		if c.providerEnabled[provider] && result.err == nil {
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

	// The rollout clock starts with the provider, not after the route is known:
	// a route that spends most of its own bound must not shorten the rollout
	// scan by handing it a remainder. Its span is a sibling of the route span,
	// so neither cancellation reaches the other or any other provider.
	fallbackSpan := c.budgets.start(c.ctx, aiResumeStageFallback)
	defer fallbackSpan.stop()

	routes, routeErr := c.catalogRoutesWithinBudget()
	validRoutes := make([]codexNativeEndpointRoute, 0, len(routes))
	for _, route := range routes {
		if route.valid() {
			validRoutes = append(validRoutes, route)
		}
	}
	// An unavailable pool source may still yield rollout rows, but it must not
	// reopen the ambient/default native catalog and accidentally grant native
	// authority. A refusing opener keeps that fallback explicitly source-only.
	if routeErr != nil || len(validRoutes) == 0 {
		if routeErr == nil {
			routeErr = &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable}
		}
		discovery, err := discover(fallbackSpan.ctx, provider, c.cwd, options(func(context.Context) (aisessions.CodexCatalog, error) {
			return nil, routeErr
		}), c.limit)
		return discovery, nil, err
	}

	var combined aisessions.ResumeSummaryDiscovery
	var failures []error
	resolvedRoutes := make([]codexNativeEndpointRoute, 0, len(validRoutes))
	for _, route := range validRoutes {
		resolvedRoutes = append(resolvedRoutes, route)
		generation, err := discover(fallbackSpan.ctx, provider, c.cwd, options(func(openCtx context.Context) (aisessions.CodexCatalog, error) {
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
	if len(combined.Summaries) != 0 || len(combined.DetailRefs) != 0 || len(failures) < len(validRoutes) {
		return c.resolveCodexCatalogCollisions(combined), resolvedRoutes, nil
	}
	return combined, nil, errors.Join(failures...)
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
	if result.err != nil || result.envelopeExpired {
		return aiResumeProviderProjection{state: aiResumeProviderSearchFailed}
	}
	count := 0
	for _, summary := range result.discovery.Summaries {
		if normalizeAIMode(summary.Provider) == normalizeAIMode(result.provider) {
			count++
		}
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
	displayLabel := aiResumeDisplayLabelForSummary(summary, c.labels, c.locale)
	loading := aiResumeDetailProjection(c.locale, summary, detailRef, aisessions.ResumeDetail{}, localizeUIText(c.locale, "Loading preview…"), displayLabel)
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
		text := aiResumeDetailProjection(c.locale, summary, detailRef, detail, previewText, displayLabel)
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

func aiResumeDetailProjection(locale i18n.Locale, summary aisessions.ResumeSummary, ref aisessions.ResumeDetailRef, detail aisessions.ResumeDetail, previewText string, displayLabels ...string) string {
	idLabel := localizeText(locale, i18n.Key("picker.ai.resume_detail_id"), "Conversation ID")
	sourceLabel := localizeUIText(locale, "Source")
	turnsLabel := localizeText(locale, i18n.KeyPickerResumeDetailTurns, "Turns")
	runtimeLabel := localizeUIText(locale, "Runtime")
	confidenceLabel := localizeText(locale, i18n.KeyPickerResumeDetailConfidence, "Confidence")
	reasonLabel := localizeText(locale, i18n.KeyPickerResumeDetailReason, "Reason")
	detailsLabel := localizeText(locale, i18n.Key("picker.ai.resume_detail_metadata"), "Details")
	unavailable := localizeUIText(locale, "unavailable")
	provider := strings.TrimSpace(summary.Provider)
	title := ""
	if len(displayLabels) > 0 {
		title = strings.TrimSpace(displayLabels[0])
	}
	if title == "" {
		title = aiResumeDisplayLabelForSummary(summary, nil, locale)
	}
	lines := []string{provider + " · " + title}
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
	return strings.Join(lines, "\n")
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
