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

	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

const (
	aiResumePreviewTimeout          = 2 * time.Second
	aiResumeSummaryPopulationBudget = 450 * time.Millisecond
	// This is wider than aisessions' 25ms scanner handoff so the provider
	// result has 10ms to cross the outer channel, while the whole first frame
	// remains bounded to 450ms + 35ms.
	aiResumeSummaryCancellationSettlementBudget = 35 * time.Millisecond
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
	events            chan struct{}
	startOnce         sync.Once
	labelsOnce        sync.Once
	labels            map[string]string

	mu             sync.Mutex
	summaries      []aisessions.ResumeSummary
	detailRefs     map[string]aisessions.ResumeDetailRef
	providerStates map[string]aiResumeProviderState
	previewText    map[string]string
	previewCache   map[aiResumePreviewKey]string
	previewCancel  context.CancelFunc
	previewSerial  uint64
	focusedValue   string
	moreNotLoaded  bool
}

func newAIResumeLiveController(cmd *aiCommand, cwd, home string, depth, limit int) *aiResumeLiveController {
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Time{}
	if cmd.now != nil {
		now = cmd.now()
	}
	return &aiResumeLiveController{
		cmd: cmd, ctx: ctx, cancel: cancel, cwd: cwd, home: home, depth: depth,
		limit: normalizeResumePickerLimit(limit), locale: appLocale(cmd.homeDir, cmd.lookupEnv), now: now,
		previewTimeout: aiResumePreviewTimeout, populationTimeout: aiResumeSummaryPopulationBudget,
		populationContext: context.WithTimeout,
		events:            make(chan struct{}, 16), providerStates: map[string]aiResumeProviderState{},
		detailRefs: map[string]aisessions.ResumeDetailRef{}, previewText: map[string]string{}, previewCache: map[aiResumePreviewKey]string{},
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
	err             error
	envelopeExpired bool
}

type aiResumeProviderState string

const (
	aiResumeProviderAvailable   aiResumeProviderState = "available"
	aiResumeProviderEmpty       aiResumeProviderState = "empty"
	aiResumeProviderFallback    aiResumeProviderState = "fallback"
	aiResumeProviderUnavailable aiResumeProviderState = "unavailable"
)

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

	discover := c.cmd.discoverResumeSummaryProvider
	if discover == nil {
		discover = aisessions.DiscoverResumeSummariesContext
	}
	providers := []string{aiModeCodex, aiModeClaude, aiModeAntigravity}
	results := make(chan aiResumeProviderResult, len(providers))
	for _, provider := range providers {
		go func() {
			discovery, err := discover(ctx, provider, c.cwd, aisessions.ResumeSummaryOptions{
				DiscoverOptions: aisessions.DiscoverOptions{
					HomeDir: c.home, Depth: c.depth, DeferTurns: true, OpenCodexCatalog: c.cmd.openCodexCatalog,
				},
			}, c.limit)
			results <- aiResumeProviderResult{provider: provider, discovery: discovery, err: err}
		}()
	}

	settled := make(map[string]aiResumeProviderResult, len(providers))
	for len(settled) < len(providers) {
		select {
		case result := <-results:
			if ctx.Err() != nil {
				result.envelopeExpired = errors.Is(result.err, context.Canceled) || errors.Is(result.err, context.DeadlineExceeded)
			}
			settled[result.provider] = result
		case <-ctx.Done():
			// Provider cancellation and its buffered result send are separate
			// scheduler events. Reserve a bounded handoff window after the 450ms
			// discovery envelope so a cancellation-settled partial is not replaced
			// by available-empty solely because its send lost the select race.
			settlementTimer := time.NewTimer(aiResumeSummaryCancellationSettlementBudget)
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
	allDetailRefs := make(map[string]aisessions.ResumeDetailRef)
	providerStates := make(map[string]aiResumeProviderState, len(providers))
	moreNotLoaded := false
	for _, provider := range providers {
		result := settled[provider]
		providerStates[provider] = resumeProviderState(result)
		if result.err == nil {
			summaries = append(summaries, result.discovery.Summaries...)
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
		c.providerStates = providerStates
		c.moreNotLoaded = moreNotLoaded
	}
	c.mu.Unlock()
}

func resumeProviderState(result aiResumeProviderResult) aiResumeProviderState {
	if result.err != nil && !result.envelopeExpired {
		return aiResumeProviderUnavailable
	}
	if normalizeAIMode(result.provider) == aiModeCodex {
		if result.discovery.Codex.Source == aisessions.CatalogSourceFallback {
			return aiResumeProviderFallback
		}
		for _, summary := range result.discovery.Summaries {
			if normalizeAIMode(summary.Provider) == aiModeCodex && summary.Source == aisessions.SourceCodexRollout {
				return aiResumeProviderFallback
			}
		}
	}
	for _, summary := range result.discovery.Summaries {
		if normalizeAIMode(summary.Provider) == normalizeAIMode(result.provider) {
			return aiResumeProviderAvailable
		}
	}
	return aiResumeProviderEmpty
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
	var hasCodex bool
	for _, summary := range summaries {
		if normalizeAIMode(summary.Provider) == aiModeCodex {
			hasCodex = true
			break
		}
	}
	if hasCodex {
		c.labelsOnce.Do(func() { c.labels = c.cmd.resolveAIResumeSummaryLabels(summaries) })
	}
	labels := c.labels
	return aiResumeSummaryRowsWithLabels(summaries, labels, c.now, c.locale, c.cwd, c.depth)
}

func (c *aiResumeLiveController) chrome() ([]intpicker.ChromeBand, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	states := make(map[string]aiResumeProviderState, len(c.providerStates))
	maps.Copy(states, c.providerStates)
	return resumeProviderChromeBands(states, c.locale), c.moreNotLoaded
}

func resumeProviderChromeBands(states map[string]aiResumeProviderState, locale i18n.Locale) []intpicker.ChromeBand {
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
		state := states[provider.id]
		if state == "" {
			state = aiResumeProviderEmpty
		}
		parts = append(parts, provider.label+" "+resumeProviderStateText(locale, state))
	}
	return []intpicker.ChromeBand{{
		Label: localizeUIText(locale, "Providers"),
		Value: strings.Join(parts, " · "),
	}}
}

func resumeProviderStateText(locale i18n.Locale, state aiResumeProviderState) string {
	text, err := i18n.NewLocalizer(locale).Text(i18n.Key("picker.ai.resume_provider_" + string(state)))
	if err != nil {
		return string(state)
	}
	return text.String()
}

func (c *aiResumeLiveController) update() (intpicker.DeferredUpdate, error) {
	c.mu.Lock()
	preview := make(map[string]string, len(c.previewText))
	maps.Copy(preview, c.previewText)
	moreNotLoaded := c.moreNotLoaded
	visible := len(c.summaries)
	c.mu.Unlock()
	footer := fmt.Sprintf(localizeUIText(c.locale, "Showing latest %d resume sessions."), visible)
	return intpicker.DeferredUpdate{
		Preview: intpicker.Preview{Window: "down,35%,border-top", TextByValue: preview},
		Footer:  projmuxFooter(footer), SetFooter: true,
		MoreNotLoaded: moreNotLoaded, SetMoreNotLoaded: true,
	}, nil
}

func (c *aiResumeLiveController) focus(value string) {
	c.mu.Lock()
	if value == c.focusedValue {
		c.mu.Unlock()
		return
	}
	c.focusedValue = value
	c.previewSerial++
	serial := c.previewSerial
	if c.previewCancel != nil {
		c.previewCancel()
		c.previewCancel = nil
	}
	c.previewText = map[string]string{}
	selection, ok := parseAIResumePickerValue(value)
	if !ok {
		c.mu.Unlock()
		c.signal()
		return
	}
	var summary aisessions.ResumeSummary
	var detailRef aisessions.ResumeDetailRef
	found := false
	for _, candidate := range c.summaries {
		if normalizeAIMode(candidate.Provider) == normalizeAIMode(selection.agent) && strings.TrimSpace(candidate.ResumeID) == selection.resumeID {
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
	if cached, hit := c.previewCache[cacheKey]; hit {
		c.previewText[value] = cached
		c.mu.Unlock()
		c.signal()
		return
	}
	timeout := c.previewTimeout
	if timeout <= 0 {
		timeout = aiResumePreviewTimeout
	}
	previewCtx, cancel := context.WithTimeout(c.ctx, timeout)
	c.previewCancel = cancel
	c.previewText[value] = localizeUIText(c.locale, "Loading preview…")
	c.mu.Unlock()
	c.signal()
	go func() {
		defer cancel()
		readPreview := c.cmd.readResumePreview
		if readPreview == nil {
			readPreview = aisessions.ReadResumeDetailPreview
		}
		preview, err := readPreview(previewCtx, detailRef, c.cmd.openCodexCatalog)
		text := aisessions.FormatPreview(preview)
		if err != nil || strings.TrimSpace(text) == "" {
			text = localizeUIText(c.locale, "preview unavailable")
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if serial != c.previewSerial || value != c.focusedValue || c.ctx.Err() != nil {
			return
		}
		c.previewText = map[string]string{value: text}
		c.previewCache[cacheKey] = text
		c.signal()
	}()
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
