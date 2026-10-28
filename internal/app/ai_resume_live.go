package app

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

const (
	aiResumePreviewTimeout          = 2 * time.Second
	aiResumeSummaryPopulationBudget = mutationruntime.ResumeSummaryPopulationBudget
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
	// handoffTimer is a private clock seam. Nil retains the declared real timer;
	// tests can order envelope expiry and partial delivery without a timer race.
	handoffTimer func(time.Duration) (<-chan time.Time, func())
	events       chan struct{}
	startOnce    sync.Once
	labelsOnce   sync.Once
	labels       map[string]aiResumeExactAgentLabel

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

type aiResumeProviderResult = mutationruntime.ResumeProviderResult

type aiResumeProviderState = mutationruntime.ResumeProviderState

const (
	aiResumeProviderCount          = mutationruntime.ResumeProviderCount
	aiResumeProviderSearchFailed   = mutationruntime.ResumeProviderSearchFailed
	aiResumeProviderScanUnfinished = mutationruntime.ResumeProviderScanUnfinished
	aiResumeProviderDisabled       = mutationruntime.ResumeProviderDisabled
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
	frame := c.discovery().Populate()
	providerStates := make(map[string]aiResumeProviderProjection, len(frame.Results))
	for provider, result := range frame.Results {
		providerStates[provider] = resumeProviderProjection(result, c.providerEnabled[provider])
	}
	c.mu.Lock()
	if c.ctx.Err() == nil {
		c.summaries = frame.Summaries
		c.detailRefs = frame.DetailRefs
		// Route inventory is published with the frame it belongs to. A route
		// that resolves after this invocation settled is discarded instead of
		// mutating the preview lane behind an already immutable row set.
		for _, route := range frame.Routes {
			c.catalogRoutes[codexCatalogRouteKey(route.Endpoint)] = localNativeCodexRoute(route)
		}
		c.providerStates = providerStates
		c.moreNotLoaded = frame.MoreNotLoaded
	}
	c.mu.Unlock()
}

// discovery is this invocation's resume candidate discovery, read on the
// controller's clocks and seams.
func (c *aiResumeLiveController) discovery() *mutationruntime.ResumeDiscovery {
	var native mutationruntime.ResumeCodexCatalog
	if c.cmd.codexNative != nil {
		native = resumeCodexCatalog{native: c.cmd.codexNative}
	}
	return mutationruntime.NewResumeDiscovery(mutationruntime.ResumeDiscoveryOptions{
		Ctx: c.ctx, Cwd: c.cwd, Home: c.home, Depth: c.depth, Limit: c.limit, ProviderEnabled: c.providerEnabled,
		PopulationTimeout: c.populationTimeout, PopulationContext: c.populationContext, Budgets: c.budgets, HandoffTimer: c.handoffTimer,
		CodexNative: native, OpenCodexCatalog: c.cmd.openCodexCatalog, LoadRegistry: c.cmd.loadRegistry, Discover: c.cmd.discoverResumeSummaryProvider,
		RecordElapsed: c.recordProviderElapsed,
	})
}

// resumeCodexCatalog reads the controller's generation inventory as the
// routes the service discovery takes.
type resumeCodexCatalog struct{ native codexNativeThreadController }

func (r resumeCodexCatalog) CatalogRoutes(ctx context.Context) ([]mutationruntime.NativeCodexRoute, error) {
	routes, err := r.native.CatalogRoutes(ctx)
	shared := make([]mutationruntime.NativeCodexRoute, 0, len(routes))
	for _, route := range routes {
		shared = append(shared, route.shared())
	}
	return shared, err
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

func codexCatalogRouteKey(endpoint coremetadata.CodexEndpointRef) string {
	return endpoint.StateDomainID + "\x00" + endpoint.EndpointGenerationID
}

func codexDetailEndpoint(ref aisessions.ResumeDetailRef) coremetadata.CodexEndpointRef {
	return coremetadata.CodexEndpointRef{StateDomainID: strings.TrimSpace(ref.StateDomainID), EndpointGenerationID: strings.TrimSpace(ref.EndpointGenerationID)}
}

func resumeProviderProjection(result aiResumeProviderResult, enabled bool) aiResumeProviderProjection {
	state, count := mutationruntime.ResumeProviderOutcome(result, enabled)
	return aiResumeProviderProjection{state: state, count: count}
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
