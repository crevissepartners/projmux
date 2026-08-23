package app

import (
	"context"
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

const aiResumePreviewTimeout = 2 * time.Second

type aiResumePreviewKey struct {
	provider  string
	id        string
	updatedAt time.Time
}

type aiResumeLiveController struct {
	cmd            *aiCommand
	ctx            context.Context
	cancel         context.CancelFunc
	cwd            string
	depth          int
	limit          int
	home           string
	locale         i18n.Locale
	now            time.Time
	previewTimeout time.Duration
	events         chan struct{}
	startOnce      sync.Once
	labelsOnce     sync.Once
	labels         map[string]string

	mu             sync.Mutex
	sessions       []aisessions.SessionMeta
	providerDone   map[string]bool
	providerFailed map[string]bool
	pendingEnrich  map[string][]aisessions.SessionMeta
	enrichStarted  map[string]bool
	previewText    map[string]string
	previewCache   map[aiResumePreviewKey]string
	previewCancel  context.CancelFunc
	previewSerial  uint64
	focusedValue   string
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
		previewTimeout: aiResumePreviewTimeout,
		events:         make(chan struct{}, 16), providerDone: map[string]bool{}, providerFailed: map[string]bool{},
		pendingEnrich: map[string][]aisessions.SessionMeta{}, enrichStarted: map[string]bool{},
		previewText: map[string]string{}, previewCache: map[aiResumePreviewKey]string{},
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
	select {
	case c.events <- struct{}{}:
	default:
	}
}

func (c *aiResumeLiveController) start() {
	c.startOnce.Do(func() {
		for _, provider := range []string{aiModeCodex, aiModeClaude, aiModeAntigravity} {
			go c.discover(provider)
		}
	})
}

func (c *aiResumeLiveController) discover(provider string) {
	discover := c.cmd.discoverResumeProvider
	if discover == nil {
		discover = aisessions.DiscoverProviderContext
	}
	discovery, err := discover(c.ctx, provider, c.cwd, aisessions.DiscoverOptions{
		HomeDir: c.home, Depth: c.depth, DeferTurns: true, OpenCodexCatalog: c.cmd.openCodexCatalog,
	}, c.limit)
	sessions := discovery.Sessions
	c.mu.Lock()
	if c.ctx.Err() == nil {
		c.providerDone[provider] = true
		c.providerFailed[provider] = err != nil
		if err == nil {
			c.sessions = append(c.sessions, sessions...)
			c.sessions = dedupeAIResumeSessions(c.sessions)
			if len(sessions) > 0 {
				c.pendingEnrich[provider] = append([]aisessions.SessionMeta(nil), sessions...)
			}
		}
	}
	c.mu.Unlock()
	c.signal()
}

func dedupeAIResumeSessions(sessions []aisessions.SessionMeta) []aisessions.SessionMeta {
	byKey := make(map[string]aisessions.SessionMeta, len(sessions))
	for _, session := range sessions {
		key := normalizeAIMode(session.Agent) + "\x00" + strings.TrimSpace(session.ResumeID)
		if old, ok := byKey[key]; !ok || session.LastModified.After(old.LastModified) {
			byKey[key] = session
		}
	}
	result := make([]aisessions.SessionMeta, 0, len(byKey))
	for _, session := range byKey {
		result = append(result, session)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].LastModified.Equal(result[j].LastModified) {
			if result[i].Agent == result[j].Agent {
				return result[i].ResumeID < result[j].ResumeID
			}
			return result[i].Agent < result[j].Agent
		}
		return result[i].LastModified.After(result[j].LastModified)
	})
	return result
}

func (c *aiResumeLiveController) initialEntries() []intpickercompat.Entry {
	return c.entries(nil, map[string]bool{}, map[string]bool{})
}

func (c *aiResumeLiveController) entries(sessions []aisessions.SessionMeta, done, failed map[string]bool) []intpickercompat.Entry {
	var hasCodex bool
	for _, session := range sessions {
		if normalizeAIMode(session.Agent) == aiModeCodex {
			hasCodex = true
			break
		}
	}
	if hasCodex {
		c.labelsOnce.Do(func() { c.labels = c.cmd.resolveAIResumeConversationLabels(sessions) })
	}
	labels := c.labels
	rows, _, _ := aiResumeSessionRowsWithLabels(sessions, labels, c.limit, c.now, c.locale, c.cwd, c.depth)
	for _, provider := range []string{aiModeCodex, aiModeClaude, aiModeAntigravity} {
		status := "loading…"
		if done[provider] {
			status = "available · no conversations"
			if failed[provider] {
				status = "unavailable"
			}
			for _, session := range sessions {
				if normalizeAIMode(session.Agent) == provider {
					status = "available"
					break
				}
			}
		}
		rows = append(rows, intpickercompat.Entry{
			Label:     fmt.Sprintf("\x1b[2m[%s] %s\x1b[0m", provider, localizeUIText(c.locale, status)),
			Value:     "status\t" + provider,
			SearchKey: provider + " " + status,
		})
	}
	return rows
}

func (c *aiResumeLiveController) update() (intpicker.DeferredUpdate, error) {
	c.start()
	c.mu.Lock()
	sessions := append([]aisessions.SessionMeta(nil), c.sessions...)
	done := cloneBoolMap(c.providerDone)
	failed := cloneBoolMap(c.providerFailed)
	preview := make(map[string]string, len(c.previewText))
	maps.Copy(preview, c.previewText)
	toEnrich := make(map[string][]aisessions.SessionMeta)
	for provider, candidates := range c.pendingEnrich {
		if !c.enrichStarted[provider] {
			c.enrichStarted[provider] = true
			toEnrich[provider] = append([]aisessions.SessionMeta(nil), candidates...)
		}
	}
	c.mu.Unlock()
	entries := c.entries(sessions, done, failed)
	visible := min(len(sessions), c.limit)
	footer := fmt.Sprintf(localizeUIText(c.locale, "Showing latest %d resume sessions."), visible)
	update := intpicker.DeferredUpdate{
		Items:   intpickercompat.PickerItemsFromEntries(entries),
		Preview: intpicker.Preview{Window: "down,35%,border-top", TextByValue: preview},
		Footer:  projmuxFooter(footer), SetFooter: true,
	}
	for provider, candidates := range toEnrich {
		go c.enrichTurns(provider, candidates)
	}
	return update, nil
}

func (c *aiResumeLiveController) enrichTurns(_ string, sessions []aisessions.SessionMeta) {
	enrich := c.cmd.enrichResumeTurns
	if enrich == nil {
		enrich = aisessions.EnrichTurns
	}
	enriched := enrich(sessions)
	c.mu.Lock()
	changed := false
	if c.ctx.Err() == nil {
		for _, candidate := range enriched {
			if candidate.Turns <= 0 {
				continue
			}
			for i := range c.sessions {
				if normalizeAIMode(c.sessions[i].Agent) == normalizeAIMode(candidate.Agent) && strings.TrimSpace(c.sessions[i].ResumeID) == strings.TrimSpace(candidate.ResumeID) && c.sessions[i].Turns != candidate.Turns {
					c.sessions[i].Turns = candidate.Turns
					changed = true
					break
				}
			}
		}
	}
	c.mu.Unlock()
	if changed {
		c.signal()
	}
}

func cloneBoolMap(source map[string]bool) map[string]bool {
	result := make(map[string]bool, len(source))
	maps.Copy(result, source)
	return result
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
	var session aisessions.SessionMeta
	found := false
	for _, candidate := range c.sessions {
		if normalizeAIMode(candidate.Agent) == normalizeAIMode(selection.agent) && strings.TrimSpace(candidate.ResumeID) == selection.resumeID {
			session, found = candidate, true
			break
		}
	}
	if !found {
		c.mu.Unlock()
		c.signal()
		return
	}
	cacheKey := aiResumePreviewCacheKey(session)
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
			readPreview = aisessions.ReadPreview
		}
		preview, err := readPreview(previewCtx, session, c.cmd.openCodexCatalog)
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

func aiResumePreviewCacheKey(session aisessions.SessionMeta) aiResumePreviewKey {
	updatedAt := session.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = session.LastModified
	}
	return aiResumePreviewKey{provider: normalizeAIMode(strings.ToLower(strings.TrimSpace(session.Agent))), id: strings.TrimSpace(session.ResumeID), updatedAt: updatedAt}
}

func (c *aiResumeLiveController) snapshotSessions() []aisessions.SessionMeta {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]aisessions.SessionMeta(nil), c.sessions...)
}
