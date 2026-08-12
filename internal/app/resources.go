package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/crevissepartners/projmux/internal/core/paneidentity"
	coreresources "github.com/crevissepartners/projmux/internal/core/resources"
	"github.com/crevissepartners/projmux/internal/i18n"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
)

const (
	resourceInspectorPopupMode = "resource-inspector"
	resourceRefreshInterval    = 2 * time.Second
)

type resourceSnapshotCollector interface {
	CollectResourceSnapshot(context.Context, *coreresources.Sample) (coreresources.Snapshot, coreresources.Sample, error)
}

type resourceCommand struct {
	collector resourceSnapshotCollector
	picker    intpicker.Runner
	homeDir   func() (string, error)
	lookupEnv func(string) string
	now       func() time.Time
	interval  time.Duration
}

type resourceText struct{ locale i18n.Locale }

func (t resourceText) value(key i18n.Key, fallback string) string {
	return localizeText(t.locale, key, fallback)
}

func (t resourceText) format(key i18n.Key, fallback string, replacements ...string) string {
	return strings.NewReplacer(replacements...).Replace(t.value(key, fallback))
}

func (t resourceText) status(status coreresources.Status) string {
	switch status {
	case coreresources.StatusReady:
		return t.value(i18n.KeyPickerResourcesStatusReady, "ready")
	case coreresources.StatusPartial:
		return t.value(i18n.KeyPickerResourcesStatusPartial, "partial")
	case coreresources.StatusUnavailable:
		return t.value(i18n.KeyPickerResourcesUnavailable, "unavailable")
	default:
		return t.value(i18n.KeyPickerResourcesStatusWarming, "warming")
	}
}

func newResourceCommand() *resourceCommand {
	return &resourceCommand{
		collector: newPlatformResourceCollector(),
		picker:    intpicker.NativeRunner{In: os.Stdin, Out: os.Stdout},
		homeDir:   os.UserHomeDir,
		lookupEnv: os.Getenv,
		now:       time.Now,
		interval:  resourceRefreshInterval,
	}
}

func (c *resourceCommand) Run(args []string, stdout, stderr io.Writer) error {
	text := resourceText{locale: appLocale(c.homeDir, c.lookupEnv)}
	if hasHelpArg(args) {
		printResourcesUsage(stdout, text)
		return nil
	}
	fs := flag.NewFlagSet("resources", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		printResourcesUsage(stderr, text)
		return usageError("resources does not accept positional arguments")
	}
	if c.collector == nil {
		return errors.New("resources collector is not configured")
	}
	if c.picker == nil {
		return errors.New("resources picker is not configured")
	}

	lifecycle := newResourceLifecycle(c.collector, c.currentTime, c.refreshInterval())
	lifecycle.start()
	defer lifecycle.close()

	view := newResourceViewState(c.currentTime, text.locale)
	for {
		options := c.pickerOptions(view, lifecycle)
		result, err := c.picker.Run(options)
		if err != nil {
			return fmt.Errorf("run resources picker: %w", err)
		}
		if result.Closed {
			if (result.Key == "esc" || result.Key == "back") && view.back() {
				continue
			}
			return nil
		}
		if result.Key == "enter" {
			view.enter(result.Value)
			continue
		}
	}
}

func hasHelpArg(args []string) bool {
	return slices.Contains(args, "-h") || slices.Contains(args, "--help") || slices.Contains(args, "help")
}

func printResourcesUsage(w io.Writer, text resourceText) {
	fmt.Fprintf(w, "%s: projmux resources\n", text.value(i18n.KeyHelpUsageCommand, "Usage"))
	fmt.Fprintln(w, "  "+text.value("picker.resources.help", "Open the read-only Project → Window → Pane resource inspector."))
}

func (c *resourceCommand) refreshInterval() time.Duration {
	if c.interval <= 0 {
		return resourceRefreshInterval
	}
	return c.interval
}

func (c *resourceCommand) currentTime() time.Time {
	if c.now == nil {
		return time.Now()
	}
	return c.now()
}

func (c *resourceCommand) pickerOptions(view *resourceViewState, lifecycle *resourceLifecycle) intpicker.Options {
	screen := view.screen()
	text := view.text
	actions := c.resourceActions(view, lifecycle, screen.actionable)
	options := intpicker.Options{
		UI:            "resources",
		Title:         screen.title,
		Prompt:        text.value(i18n.KeyPickerResourcesPrompt, "search › "),
		ChromeBands:   screen.bands,
		Footer:        screen.footer,
		Items:         screen.items,
		MultiLine:     true,
		DisableSearch: !screen.actionable,
		ReadOnly:      !screen.actionable,
		Locale:        text.locale,
		Actions:       actions,
		DeferredUpdate: func() (intpicker.DeferredUpdate, error) {
			return c.withResourceActions(view, lifecycle, lifecycle.collect(view)), nil
		},
		DeferredUpdateTrigger: lifecycle.trigger,
	}
	if source, err := configRenderThemeSource(c.homeDir, c.lookupEnv, ""); err == nil {
		return source.pickerOptions(options)
	}
	return fallbackRenderThemeSource().pickerOptions(options)
}

func (c *resourceCommand) resourceActions(view *resourceViewState, lifecycle *resourceLifecycle, actionable bool) []intpicker.Action {
	actions := pickerCloseActionsForPopupToggleMode(c.homeDir, c.lookupEnv, resourceInspectorPopupMode, "esc")
	if actionable {
		actions = append(actions, intpicker.Action{Key: "tab", Intent: intpicker.ActionCustom, Mutate: func(intpicker.ActionContext) (intpicker.DeferredUpdate, error) {
			view.cycleSort()
			return c.withResourceActions(view, lifecycle, view.deferredUpdate()), nil
		}})
	}
	actions = append(actions,
		intpicker.Action{Key: "ctrl-r", Intent: intpicker.ActionCustom, Mutate: func(intpicker.ActionContext) (intpicker.DeferredUpdate, error) {
			return c.withResourceActions(view, lifecycle, lifecycle.collect(view)), nil
		}},
		intpicker.Action{Key: "alt-left", Intent: intpicker.ActionCustom, Mutate: func(ctx intpicker.ActionContext) (intpicker.DeferredUpdate, error) {
			return intpicker.DeferredUpdate{Result: &intpicker.Result{Key: "back", Query: ctx.Query, Closed: true}}, nil
		}},
	)
	return actions
}

func (c *resourceCommand) withResourceActions(view *resourceViewState, lifecycle *resourceLifecycle, update intpicker.DeferredUpdate) intpicker.DeferredUpdate {
	screen := view.screen()
	update.Actions = c.resourceActions(view, lifecycle, screen.actionable)
	update.SetActions = true
	return update
}

type resourceLifecycle struct {
	collector resourceSnapshotCollector
	now       func() time.Time
	interval  time.Duration
	ctx       context.Context
	cancel    context.CancelFunc
	trigger   chan struct{}
	gate      chan struct{}
	skipped   atomic.Uint64
	mu        sync.Mutex
	closing   bool
	active    sync.WaitGroup
	done      chan struct{}
	previous  *coreresources.Sample
}

func newResourceLifecycle(collector resourceSnapshotCollector, now func() time.Time, interval time.Duration) *resourceLifecycle {
	ctx, cancel := context.WithCancel(context.Background())
	return &resourceLifecycle{collector: collector, now: now, interval: interval, ctx: ctx, cancel: cancel, trigger: make(chan struct{}, 1), gate: make(chan struct{}, 1), done: make(chan struct{})}
}

func (l *resourceLifecycle) start() {
	l.trigger <- struct{}{}
	go func() {
		defer close(l.done)
		ticker := time.NewTicker(l.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				select {
				case l.trigger <- struct{}{}:
				default:
					l.skipped.Add(1)
				}
			case <-l.ctx.Done():
				return
			}
		}
	}()
}

func (l *resourceLifecycle) begin() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closing {
		return false
	}
	l.active.Add(1)
	return true
}

func (l *resourceLifecycle) collect(view *resourceViewState) intpicker.DeferredUpdate {
	if !l.begin() {
		return view.deferredUpdate()
	}
	defer l.active.Done()
	select {
	case l.gate <- struct{}{}:
		defer func() { <-l.gate }()
	default:
		l.skipped.Add(1)
		view.setSkippedRefresh(true)
		return view.deferredUpdate()
	}
	before := l.skipped.Load()
	snapshot, current, err := l.collector.CollectResourceSnapshot(l.ctx, l.previous)
	if err != nil {
		snapshot = coreresources.Snapshot{At: l.now(), Status: coreresources.StatusUnavailable, StatusReason: "collection-error"}
	} else if l.ctx.Err() == nil {
		l.previous = &current
	}
	view.setSnapshot(snapshot, l.skipped.Load() > before)
	return view.deferredUpdate()
}

func (l *resourceLifecycle) close() {
	l.mu.Lock()
	l.closing = true
	l.mu.Unlock()
	l.cancel()
	<-l.done
	l.active.Wait()
	l.previous = nil
}

type resourceScopeKind uint8

const (
	resourceScopeProjects resourceScopeKind = iota
	resourceScopeWindows
	resourceScopePanes
	resourceScopePaneDetail
)

type resourceSort uint8

const (
	resourceSortCPU resourceSort = iota
	resourceSortMemory
	resourceSortName
)

type resourceScope struct {
	kind       resourceScopeKind
	projectKey string
	socket     string
	windowID   string
	paneID     string
}

type resourceViewState struct {
	mu      sync.RWMutex
	now     func() time.Time
	scope   resourceScope
	sort    resourceSort
	snap    coreresources.Snapshot
	skipped bool
	text    resourceText
}

func newResourceViewState(now func() time.Time, locale i18n.Locale) *resourceViewState {
	return &resourceViewState{now: now, text: resourceText{locale: locale}, snap: coreresources.Snapshot{Status: coreresources.StatusWarming, StatusReason: "first-sample-pending"}}
}

func (v *resourceViewState) setSnapshot(snapshot coreresources.Snapshot, skipped bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.snap = snapshot
	v.skipped = skipped
}

func (v *resourceViewState) setSkippedRefresh(skipped bool) {
	v.mu.Lock()
	v.skipped = skipped
	v.mu.Unlock()
}

func (v *resourceViewState) cycleSort() {
	v.mu.Lock()
	v.sort = (v.sort + 1) % 3
	v.mu.Unlock()
}

func (v *resourceViewState) back() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	switch v.scope.kind {
	case resourceScopePaneDetail:
		v.scope.kind = resourceScopePanes
		v.scope.paneID = ""
	case resourceScopePanes:
		v.scope.kind = resourceScopeWindows
		v.scope.windowID = ""
	case resourceScopeWindows:
		v.scope = resourceScope{}
	default:
		return false
	}
	return true
}

func (v *resourceViewState) enter(value string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	switch v.scope.kind {
	case resourceScopeProjects:
		if value == "project:"+coreresources.OtherUnattributed || !strings.HasPrefix(value, "project:") {
			return
		}
		v.scope.kind = resourceScopeWindows
		v.scope.projectKey = strings.TrimPrefix(value, "project:")
	case resourceScopeWindows:
		socket, id, ok := parseResourceStableValue(value, "window:")
		if !ok {
			return
		}
		v.scope.kind, v.scope.socket, v.scope.windowID = resourceScopePanes, socket, id
	case resourceScopePanes:
		socket, id, ok := parseResourceStableValue(value, "pane:")
		if !ok {
			return
		}
		v.scope.kind, v.scope.socket, v.scope.paneID = resourceScopePaneDetail, socket, id
	}
}

func parseResourceStableValue(value, prefix string) (string, string, bool) {
	raw, ok := strings.CutPrefix(value, prefix)
	if !ok {
		return "", "", false
	}
	socket, id, ok := strings.Cut(raw, "\x1f")
	return socket, id, ok && id != ""
}

func (v *resourceViewState) deferredUpdate() intpicker.DeferredUpdate {
	screen := v.screen()
	return intpicker.DeferredUpdate{Items: screen.items, ChromeBands: screen.bands, SetChromeBands: true, Footer: screen.footer, SetFooter: true, Title: screen.title, SetTitle: true, DisableSearch: !screen.actionable, ReadOnly: !screen.actionable, SetInteraction: true}
}

type resourceScreen struct {
	title      string
	bands      []intpicker.ChromeBand
	items      []intpicker.Item
	footer     string
	actionable bool
}

func (v *resourceViewState) screen() resourceScreen {
	v.mu.RLock()
	defer v.mu.RUnlock()
	items := v.itemsLocked()
	actionable := v.scope.kind != resourceScopePaneDetail && (len(items) > 0 || v.snap.Status == coreresources.StatusWarming)
	return resourceScreen{
		title:      resourceBreadcrumb(v.snap, v.scope, v.text),
		bands:      resourceSummaryBands(v.snap, v.currentTime(), v.skipped, v.scope, v.text),
		items:      items,
		footer:     resourceFooter(v.scope.kind, v.sort, actionable, v.text),
		actionable: actionable,
	}
}

func (v *resourceViewState) currentTime() time.Time {
	if v.now == nil {
		return time.Now()
	}
	return v.now()
}

type resourceRow struct {
	identity, context, value, search string
	cpu                              *coreresources.CPUUsage
	rss                              uint64
	memPercent                       *float64
	memKnown                         bool
	count                            int
	countKnown                       bool
	meta                             []string
}

func (v *resourceViewState) itemsLocked() []intpicker.Item {
	var rows []resourceRow
	switch v.scope.kind {
	case resourceScopeProjects:
		rows = resourceProjectRows(v.snap, v.text)
	case resourceScopeWindows:
		rows = resourceWindowRows(v.snap, v.scope.projectKey, v.text)
	case resourceScopePanes:
		rows = resourcePaneRows(v.snap, v.scope.socket, v.scope.windowID, v.text)
	case resourceScopePaneDetail:
		rows = resourcePaneDetailRows(v.snap, v.scope.socket, v.scope.paneID, v.text)
	}
	if v.scope.kind != resourceScopePaneDetail {
		sortResourceRows(rows, v.sort)
	}
	items := make([]intpicker.Item, 0, len(rows))
	for _, row := range rows {
		label := row.identity
		meta := append([]string(nil), row.meta...)
		if v.scope.kind != resourceScopePaneDetail {
			paneCount := v.text.value("picker.resources.row.pane_count_unknown", "-- panes")
			if row.countKnown {
				paneCount = v.text.format("picker.resources.row.pane_count", "{count} panes", "{count}", fmt.Sprint(row.count))
			}
			if row.context != "" {
				meta = append([]string{v.text.format("picker.resources.row.context", "Context  {context}", "{context}", row.context)}, meta...)
			}
			meta = append(meta, v.text.format("picker.resources.row.metrics", "CPU {cpu}  MEMORY {memory}  PANES {panes}",
				"{cpu}", fmt.Sprintf("%-8s", formatResourceCPU(row.cpu)),
				"{memory}", fmt.Sprintf("%-22s", formatResourceMemory(row.rss, row.memPercent, row.memKnown)),
				"{panes}", paneCount,
			))
		}
		items = append(items, intpicker.Item{Label: label, Title: label, Value: row.value, SearchText: row.search, MetaLines: meta})
	}
	return items
}

func resourceProjectRows(snapshot coreresources.Snapshot, text resourceText) []resourceRow {
	byKey := make(map[string]coreresources.ProjectUsage, len(snapshot.Projects))
	for _, project := range snapshot.Projects {
		byKey[project.Key] = project
	}
	keys := []string{coreresources.ProjectUnassigned, coreresources.ProjectShared}
	for _, project := range snapshot.Projects {
		if project.Key != coreresources.ProjectUnassigned && project.Key != coreresources.ProjectShared {
			keys = append(keys, project.Key)
		}
	}
	rows := make([]resourceRow, 0, len(keys))
	for _, key := range keys {
		project, ok := byKey[key]
		identity := resourceProjectDisplayName(key, text)
		context := ""
		if key != coreresources.ProjectUnassigned && key != coreresources.ProjectShared {
			identity = filepath.Base(key)
			context = key
		}
		rows = append(rows, resourceRow{identity: identity, context: context, value: "project:" + key, search: identity + " " + key, cpu: project.CPU, rss: project.Memory.RSSBytes, memPercent: project.Memory.HostPercent, memKnown: ok && snapshot.Host.MemoryAvailable, count: project.PaneCount, countKnown: ok && snapshot.Status != coreresources.StatusUnavailable})
	}
	return rows
}

func resourceProjectDisplayName(key string, text resourceText) string {
	switch key {
	case coreresources.ProjectUnassigned:
		return text.value("picker.resources.bucket.unassigned", coreresources.ProjectUnassigned)
	case coreresources.ProjectShared:
		return text.value("picker.resources.bucket.shared", coreresources.ProjectShared)
	case coreresources.OtherUnattributed:
		return text.value("picker.resources.bucket.other", coreresources.OtherUnattributed)
	default:
		return key
	}
}

func resourceWindowRows(snapshot coreresources.Snapshot, project string, text resourceText) []resourceRow {
	rows := make([]resourceRow, 0)
	for _, window := range snapshot.Windows {
		if window.ProjectKey != project {
			continue
		}
		context := strings.Join(window.Sessions, ",")
		identity := strings.TrimSpace(window.WindowName + " " + window.WindowID)
		secondary := text.format("picker.resources.context.sessions", "Session {sessions}", "{sessions}", context)
		rows = append(rows, resourceRow{identity: identity, context: secondary, value: "window:" + window.Socket + "\x1f" + window.WindowID, search: identity + " " + secondary + " " + window.WindowID, cpu: window.CPU, rss: window.Memory.RSSBytes, memPercent: window.Memory.HostPercent, memKnown: snapshot.Host.MemoryAvailable, count: window.PaneCount, countKnown: true})
	}
	return rows
}

func resourcePaneRows(snapshot coreresources.Snapshot, socket, windowID string, text resourceText) []resourceRow {
	rows := make([]resourceRow, 0)
	for _, pane := range snapshot.Panes {
		if pane.Socket != socket || pane.WindowID != windowID {
			continue
		}
		identity := paneidentity.Resolve(paneidentity.Inputs{Label: pane.PaneLabel, AIAgent: pane.AIAgent, AITopic: pane.AITopic, Command: pane.PaneCommand, Title: pane.PaneTitle}).Value
		if identity == "" {
			identity = text.value("picker.resources.unnamed_pane", "Unnamed pane")
		}
		secondary := text.format("picker.resources.context.pane", "Pane {pane} · PID {pid} · {tty}", "{pane}", pane.PaneID, "{pid}", fmt.Sprint(pane.PanePID), "{tty}", pane.PaneTTY)
		rows = append(rows, resourceRow{identity: identity, context: secondary, value: "pane:" + pane.Socket + "\x1f" + pane.PaneID, search: identity + " " + secondary + " " + pane.PaneID, cpu: pane.CPU, rss: pane.Memory.RSSBytes, memPercent: pane.Memory.HostPercent, memKnown: snapshot.Host.MemoryAvailable, count: 1, countKnown: true})
	}
	return rows
}

func resourcePaneDetailRows(snapshot coreresources.Snapshot, socket, paneID string, text resourceText) []resourceRow {
	for _, pane := range snapshot.Panes {
		if pane.Socket != socket || pane.PaneID != paneID {
			continue
		}
		cpu := formatResourceCPU(pane.CPU)
		if pane.CPU != nil {
			cpu += fmt.Sprintf(" · %.2fc", pane.CPU.CoreEquivalent)
		}
		anchor := pane.ProjectAnchor
		if anchor == "" {
			anchor = resourceProjectDisplayName(coreresources.ProjectUnassigned, text)
		}
		meta := []string{
			text.format("picker.resources.detail.project", "Project: {project}", "{project}", anchor),
			text.format("picker.resources.detail.session_window", "Session: {session}  Window: {window} {window_id}", "{session}", strings.Join(pane.Sessions, ", "), "{window}", pane.WindowName, "{window_id}", pane.WindowID),
			text.format("picker.resources.detail.pane", "Pane: {pane_id}  PID/SID: {pid}  TTY: {tty}", "{pane_id}", pane.PaneID, "{pid}", fmt.Sprint(pane.PanePID), "{tty}", pane.PaneTTY),
			text.format("picker.resources.detail.processes_cpu", "Processes: {count}  CPU host share: {cpu}", "{count}", fmt.Sprint(pane.ProcessCount), "{cpu}", cpu),
			text.format("picker.resources.detail.memory", "Memory RSS sum: {memory}", "{memory}", formatResourceMemory(pane.Memory.RSSBytes, pane.Memory.HostPercent, snapshot.Host.MemoryAvailable)),
			text.value("picker.resources.detail.rss_caveat", "RSS sum may count shared pages more than once."),
		}
		return []resourceRow{{identity: text.value("picker.resources.detail.heading", "Pane details"), value: "", meta: meta}}
	}
	return []resourceRow{{identity: text.value("picker.resources.detail.gone", "Pane no longer exists"), value: "", meta: []string{text.value("picker.resources.detail.gone_help", "The selected pane vanished during refresh. Esc returns to the nearest available pane.")}}}
}

func sortResourceRows(rows []resourceRow, order resourceSort) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		switch order {
		case resourceSortCPU:
			if (a.cpu == nil) != (b.cpu == nil) {
				return a.cpu != nil
			}
			if a.cpu != nil && a.cpu.HostSharePercent != b.cpu.HostSharePercent {
				return a.cpu.HostSharePercent > b.cpu.HostSharePercent
			}
		case resourceSortMemory:
			if a.memKnown != b.memKnown {
				return a.memKnown
			}
			if a.memKnown && a.rss != b.rss {
				return a.rss > b.rss
			}
		case resourceSortName:
			if !strings.EqualFold(a.identity, b.identity) {
				return strings.ToLower(a.identity) < strings.ToLower(b.identity)
			}
		}
		return a.value < b.value
	})
}

func resourceBreadcrumb(snapshot coreresources.Snapshot, scope resourceScope, text resourceText) string {
	title := text.value(i18n.KeyPickerResourcesTitle, "Resources") + " · " + text.value("picker.resources.scope.projects", "Projects")
	segments := make([]string, 0, 3)
	if scope.kind >= resourceScopeWindows {
		project := resourceProjectDisplayName(scope.projectKey, text)
		if scope.projectKey != coreresources.ProjectUnassigned && scope.projectKey != coreresources.ProjectShared {
			project = filepath.Base(scope.projectKey)
		}
		segments = append(segments, project)
	}
	if scope.kind >= resourceScopePanes {
		if window, ok := resourceWindow(snapshot, scope.socket, scope.windowID); ok {
			segments = append(segments, strings.TrimSpace(window.WindowName+" "+window.WindowID))
		} else {
			segments = append(segments, scope.windowID)
		}
	}
	if scope.kind == resourceScopePaneDetail {
		if pane, ok := resourcePane(snapshot, scope.socket, scope.paneID); ok {
			identity := paneidentity.Resolve(paneidentity.Inputs{Label: pane.PaneLabel, AIAgent: pane.AIAgent, AITopic: pane.AITopic, Command: pane.PaneCommand, Title: pane.PaneTitle}).Value
			if identity == "" {
				identity = text.value("picker.resources.unnamed_pane", "Unnamed pane")
			}
			segments = append(segments, strings.TrimSpace(identity+" "+pane.PaneID))
		} else {
			segments = append(segments, scope.paneID)
		}
	}
	if len(segments) == 0 {
		return title
	}
	return title + " / " + strings.Join(segments, " / ")
}

func resourceWindow(snapshot coreresources.Snapshot, socket, windowID string) (coreresources.WindowUsage, bool) {
	for _, window := range snapshot.Windows {
		if window.Socket == socket && window.WindowID == windowID {
			return window, true
		}
	}
	return coreresources.WindowUsage{}, false
}

func resourcePane(snapshot coreresources.Snapshot, socket, paneID string) (coreresources.PaneUsage, bool) {
	for _, pane := range snapshot.Panes {
		if pane.Socket == socket && pane.PaneID == paneID {
			return pane, true
		}
	}
	return coreresources.PaneUsage{}, false
}

func resourceSummaryBands(snapshot coreresources.Snapshot, now time.Time, skipped bool, scope resourceScope, text resourceText) []intpicker.ChromeBand {
	status := text.status(snapshot.Status)
	if skipped && snapshot.Status != coreresources.StatusUnavailable {
		status = text.value("picker.resources.status.refresh_skipped", "partial (refresh skipped; prior scan still in flight)")
	}
	hostMem := (*float64)(nil)
	if snapshot.Host.MemoryAvailable && snapshot.Host.MemoryTotalBytes > 0 {
		value := float64(snapshot.Host.MemoryTotalBytes-snapshot.Host.MemoryAvailableBytes) * 100 / float64(snapshot.Host.MemoryTotalBytes)
		hostMem = &value
	}
	attrMem := (*float64)(nil)
	attrRSS := "--"
	if snapshot.Host.MemoryAvailable && snapshot.Host.MemoryTotalBytes > 0 {
		value := float64(snapshot.Memory.AttributedRSSBytes) * 100 / float64(snapshot.Host.MemoryTotalBytes)
		attrMem = &value
		attrRSS = formatBytes(snapshot.Memory.AttributedRSSBytes)
	}
	age := "--"
	if !snapshot.At.IsZero() {
		d := max(now.Sub(snapshot.At), 0)
		age = d.Round(100 * time.Millisecond).String()
	}
	bands := []intpicker.ChromeBand{
		{Label: text.value("picker.resources.band.host", "Host"), Value: text.format("picker.resources.band.host.metrics", "CPU {cpu}    Memory {memory}", "{cpu}", formatResourcePercent(snapshot.CPU.HostBusyPercent), "{memory}", formatResourcePercent(hostMem)), Secondary: text.format("picker.resources.band.sample", "sample {age} · {status}", "{age}", age, "{status}", status)},
		{Label: text.value("picker.resources.band.attributed", "Attributed"), Value: text.format("picker.resources.band.attributed.metrics", "CPU {cpu}    RSS {rss} ({memory})", "{cpu}", formatResourcePercent(snapshot.CPU.AttributedPercent), "{rss}", attrRSS, "{memory}", formatResourcePercent(attrMem))},
	}
	if scope.kind == resourceScopeProjects {
		otherCPU, otherMemory := "--", "--"
		if snapshot.Other.CPUHostSharePercent != nil {
			otherCPU = formatResourcePercent(snapshot.Other.CPUHostSharePercent)
		}
		if snapshot.Other.MemoryBytes != nil {
			otherMemory = formatBytes(*snapshot.Other.MemoryBytes)
		}
		bands = append(bands, intpicker.ChromeBand{Label: resourceProjectDisplayName(coreresources.OtherUnattributed, text), Value: text.format("picker.resources.band.other.metrics", "CPU {cpu}    Memory {memory}", "{cpu}", otherCPU, "{memory}", otherMemory), Secondary: text.value("picker.resources.other_not_drillable", "summary only · not drillable")})
	}
	if diagnostic := resourceDiagnostic(snapshot, text); diagnostic != "" {
		bands = append(bands, intpicker.ChromeBand{Label: text.value("picker.resources.band.status", "Status"), Value: diagnostic})
	}
	if scope.kind == resourceScopeWindows && len(resourceWindowRows(snapshot, scope.projectKey, text)) == 0 {
		bands = append(bands, intpicker.ChromeBand{Label: text.value("picker.resources.band.empty", "Empty"), Value: text.value("picker.resources.empty.windows", "No windows in this project"), Secondary: text.value("picker.resources.empty.not_actionable", "No row to open")})
	}
	if scope.kind == resourceScopePanes && len(resourcePaneRows(snapshot, scope.socket, scope.windowID, text)) == 0 {
		bands = append(bands, intpicker.ChromeBand{Label: text.value("picker.resources.band.empty", "Empty"), Value: text.value("picker.resources.empty.panes", "No panes in this window"), Secondary: text.value("picker.resources.empty.not_actionable", "No row to open")})
	}
	return bands
}

func resourceFooter(scope resourceScopeKind, order resourceSort, actionable bool, text resourceText) string {
	sorts := []string{
		text.value("picker.resources.sort.cpu", "CPU"),
		text.value("picker.resources.sort.memory", "Memory"),
		text.value("picker.resources.sort.name", "Name"),
	}
	if scope == resourceScopePaneDetail {
		return text.value("picker.resources.footer.detail", "Read-only pane detail | Esc/Alt-Left: back | Ctrl-R: refresh")
	}
	if !actionable {
		return text.value("picker.resources.footer.empty", "Read-only | Esc/Alt-Left: back/close | Ctrl-R: refresh")
	}
	return text.format("picker.resources.footer.list", "Enter: drill down | Esc/Alt-Left: back/close | Tab: sort {sort} | Ctrl-R: refresh", "{sort}", sorts[order])
}

func resourceDiagnostic(snapshot coreresources.Snapshot, text resourceText) string {
	var diagnostics []string
	if snapshot.Status == coreresources.StatusPartial {
		d := snapshot.Diagnostics
		diagnostics = append(diagnostics, text.format("picker.resources.diagnostic.partial", "partial sampled={sampled} skipped={skipped} race={race} permission={permission}",
			"{sampled}", fmt.Sprint(d.Scan.SampledProcesses),
			"{skipped}", fmt.Sprint(d.Scan.SkippedProcesses),
			"{race}", fmt.Sprint(d.Scan.RaceCount),
			"{permission}", fmt.Sprint(d.Scan.PermissionCount),
		))
	}
	if snapshot.CPU.OveragePercent > 0 {
		diagnostics = append(diagnostics, text.format("picker.resources.diagnostic.cpu_overage", "CPU overage {overage}", "{overage}", fmt.Sprintf("%.1f%%", snapshot.CPU.OveragePercent)))
	}
	if snapshot.Memory.OverageBytes > 0 {
		diagnostics = append(diagnostics, text.format("picker.resources.diagnostic.rss_overage", "RSS overage {overage}", "{overage}", formatBytes(snapshot.Memory.OverageBytes)))
	}
	if snapshot.Status == coreresources.StatusUnavailable {
		diagnostics = append(diagnostics, resourceUnavailableReason(snapshot.StatusReason, text))
	}
	return strings.Join(diagnostics, " | ")
}

func resourceUnavailableReason(reason string, text resourceText) string {
	platform, ok := strings.CutPrefix(strings.TrimSpace(reason), "platform:")
	if ok && platform != "" {
		return text.format("picker.resources.unavailable.platform", "Resource attribution is unavailable on {platform}.", "{platform}", platform)
	}
	const legacyPrefix = "resource attribution is unavailable on "
	if platform, ok = strings.CutPrefix(strings.ToLower(strings.TrimSpace(reason)), legacyPrefix); ok && platform != "" {
		return text.format("picker.resources.unavailable.platform", "Resource attribution is unavailable on {platform}.", "{platform}", platform)
	}
	return text.value("picker.resources.unavailable.generic", "Resource attribution is unavailable.")
}

func formatResourceCPU(cpu *coreresources.CPUUsage) string {
	if cpu == nil {
		return "--"
	}
	return fmt.Sprintf("%.1f%%", cpu.HostSharePercent)
}

func formatResourceMemory(bytes uint64, percent *float64, known bool) string {
	if !known {
		return "--"
	}
	return fmt.Sprintf("%s (%s)", formatBytes(bytes), formatResourcePercent(percent))
}

func formatResourcePercent(value *float64) string {
	if value == nil {
		return "--"
	}
	return fmt.Sprintf("%.1f%%", *value)
}

func formatBytes(value uint64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	f := float64(value)
	for _, suffix := range units {
		f /= unit
		if f < unit || suffix == units[len(units)-1] {
			return fmt.Sprintf("%.1f %s", f, suffix)
		}
	}
	return fmt.Sprintf("%d B", value)
}
