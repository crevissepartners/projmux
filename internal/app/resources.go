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
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/theme"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	"github.com/crevissepartners/projmux/internal/ui/projmuxpicker"
)

const (
	resourceInspectorPopupMode = "resource-inspector"
	resourceRefreshInterval    = 2 * time.Second
	resourceScanBudget         = resourceRefreshInterval
)

type resourceSnapshotCollector interface {
	CollectResourceSnapshot(context.Context, *coreresources.Sample) (coreresources.Snapshot, coreresources.Sample, error)
}

type resourceCollectionError struct {
	source  diagnostics.ResourceSource
	failure diagnostics.ResourceFailure
	cause   error
}

func (e *resourceCollectionError) Error() string { return e.cause.Error() }
func (e *resourceCollectionError) Unwrap() error { return e.cause }

type resourceCommand struct {
	collector   resourceSnapshotCollector
	picker      intpicker.Runner
	homeDir     func() (string, error)
	lookupEnv   func(string) string
	now         func() time.Time
	interval    time.Duration
	scanBudget  time.Duration
	diagnostics *diagnostics.ResourceRecorder
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
		collector:  newPlatformResourceCollector(),
		picker:     intpicker.NativeRunner{In: os.Stdin, Out: os.Stdout},
		homeDir:    os.UserHomeDir,
		lookupEnv:  os.Getenv,
		now:        time.Now,
		interval:   resourceRefreshInterval,
		scanBudget: resourceScanBudget,
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
	lifecycle.scanBudget = c.collectionBudget()
	lifecycle.diagnostics = c.diagnostics
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
			if result.Key == "back" && view.back() {
				continue
			}
			return nil
		}
		if result.Key == "enter" || result.Key == "right" {
			view.enter(result.Value)
			continue
		}
	}
}

func (c *resourceCommand) collectionBudget() time.Duration {
	if c.scanBudget <= 0 {
		return resourceScanBudget
	}
	return c.scanBudget
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
	source, err := configRenderThemeSource(c.homeDir, c.lookupEnv, "")
	if err != nil {
		source = fallbackRenderThemeSource()
	}
	view.setPalette(newResourceSemanticPalette(source.effective))
	screen := view.screen()
	text := view.text
	actions := c.resourceActions(view, lifecycle, screen.actionable)
	options := intpicker.Options{
		UI:                  "resources",
		Title:               screen.title,
		Prompt:              text.value(i18n.KeyPickerResourcesPrompt, "› "),
		ResourceSummaryDock: screen.bands,
		Footer:              screen.footer,
		Items:               screen.items,
		MultiLine:           true,
		DisableSearch:       !screen.actionable,
		ReadOnly:            !screen.actionable,
		Locale:              text.locale,
		Actions:             actions,
		DeferredUpdate: func() (intpicker.DeferredUpdate, error) {
			return c.withResourceActions(view, lifecycle, lifecycle.collect(view)), nil
		},
		DeferredUpdateTrigger: lifecycle.trigger,
	}
	return source.pickerOptions(options)
}

func (c *resourceCommand) resourceActions(view *resourceViewState, lifecycle *resourceLifecycle, actionable bool) []intpicker.Action {
	actions := pickerCloseActionsForPopupToggleMode(c.homeDir, c.lookupEnv, resourceInspectorPopupMode, "esc")
	if actionable {
		actions = append(actions,
			intpicker.Action{Key: "right", Intent: intpicker.ActionAccept},
			intpicker.Action{Key: "tab", Intent: intpicker.ActionCustom, Mutate: func(intpicker.ActionContext) (intpicker.DeferredUpdate, error) {
				view.cycleSort()
				return c.withResourceActions(view, lifecycle, view.deferredUpdate()), nil
			}},
		)
	}
	actions = append(actions,
		intpicker.Action{Key: "left", Intent: intpicker.ActionCustom, Mutate: func(ctx intpicker.ActionContext) (intpicker.DeferredUpdate, error) {
			if view.atRoot() {
				return c.withResourceActions(view, lifecycle, view.deferredUpdate()), nil
			}
			return intpicker.DeferredUpdate{Result: &intpicker.Result{Key: "back", Query: ctx.Query, Closed: true}}, nil
		}},
		intpicker.Action{Key: "ctrl-r", Intent: intpicker.ActionCustom, Mutate: func(intpicker.ActionContext) (intpicker.DeferredUpdate, error) {
			lifecycle.request(view)
			return c.withResourceActions(view, lifecycle, view.deferredUpdate()), nil
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
	collector        resourceSnapshotCollector
	now              func() time.Time
	interval         time.Duration
	ctx              context.Context
	cancel           context.CancelFunc
	trigger          chan struct{}
	skipped          atomic.Uint64
	mu               sync.Mutex
	closing          bool
	initialized      bool
	collecting       bool
	automaticPending bool
	manualPending    bool
	completion       *resourceCollectionResult
	active           sync.WaitGroup
	done             chan struct{}
	previous         *coreresources.Sample
	lastCompleteAt   time.Time
	scanBudget       time.Duration
	diagnostics      *diagnostics.ResourceRecorder
	diagnosticsMu    sync.Mutex
	afterStateCommit func()
}

type resourceCollectionResult struct {
	snapshot     coreresources.Snapshot
	skipped      bool
	sample       coreresources.Sample
	commitSample bool
	diagnostic   resourceDiagnosticOutcome
}

type resourceDiagnosticOutcome struct {
	started time.Time
	source  diagnostics.ResourceSource
	result  diagnostics.ResourceResult
	failure diagnostics.ResourceFailure
	recover []diagnostics.ResourceSource
	healthy bool
	record  bool
}

func newResourceLifecycle(collector resourceSnapshotCollector, now func() time.Time, interval time.Duration) *resourceLifecycle {
	ctx, cancel := context.WithCancel(context.Background())
	return &resourceLifecycle{collector: collector, now: now, interval: interval, scanBudget: interval, ctx: ctx, cancel: cancel, trigger: make(chan struct{}, 1), done: make(chan struct{})}
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
				l.requestAutomatic()
			case <-l.ctx.Done():
				return
			}
		}
	}()
}

func (l *resourceLifecycle) collect(view *resourceViewState) intpicker.DeferredUpdate {
	l.mu.Lock()
	if l.closing {
		l.mu.Unlock()
		return view.deferredUpdate()
	}
	if l.completion != nil {
		result := *l.completion
		l.completion = nil
		pending := l.automaticPending || l.manualPending
		l.mu.Unlock()
		view.setSnapshot(result.snapshot, result.skipped)
		if pending {
			view.setRefreshing(true)
			l.notify()
		}
		return view.deferredUpdate()
	}
	if l.collecting {
		l.mu.Unlock()
		view.setRefreshing(true)
		return view.deferredUpdate()
	}
	async := l.initialized
	if async && !l.automaticPending && !l.manualPending {
		l.mu.Unlock()
		return view.deferredUpdate()
	}
	l.automaticPending = false
	l.manualPending = false
	l.initialized = true
	l.collecting = true
	l.active.Add(1)
	previous := l.previous
	before := l.skipped.Load()
	l.mu.Unlock()

	if async {
		view.setRefreshing(true)
		go l.runCollection(before, previous)
		return view.deferredUpdate()
	}
	result := l.collectSnapshot(before, previous)
	l.finishCollection(result, false)
	view.setSnapshot(result.snapshot, result.skipped)
	return view.deferredUpdate()
}

func (l *resourceLifecycle) runCollection(before uint64, previous *coreresources.Sample) {
	result := l.collectSnapshot(before, previous)
	l.finishCollection(result, true)
}

func (l *resourceLifecycle) collectSnapshot(before uint64, previous *coreresources.Sample) resourceCollectionResult {
	started := l.now()
	ctx := l.ctx
	cancel := func() {}
	if l.scanBudget > 0 {
		ctx, cancel = context.WithTimeout(l.ctx, l.scanBudget)
	}
	defer cancel()
	snapshot, current, err := l.collector.CollectResourceSnapshot(ctx, previous)
	budgetExceeded := false
	var diagnostic resourceDiagnosticOutcome
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		budgetExceeded = true
		snapshot = coreresources.Snapshot{At: l.now(), Status: coreresources.StatusUnavailable, StatusReason: "scan-budget-exceeded"}
		diagnostic = resourceDiagnosticOutcome{
			started: started, source: diagnostics.ResourceSourceSampler, result: diagnostics.ResourceResultScanBudgetExceeded,
			failure: diagnostics.ResourceFailureScanBudget, record: true,
		}
		err = nil
	} else if l.ctx.Err() == nil {
		diagnostic = classifyResourceOutcome(snapshot, err, started)
	}
	if err != nil {
		snapshot = coreresources.Snapshot{At: l.now(), Status: coreresources.StatusUnavailable, StatusReason: "collection-error"}
	}
	result := resourceCollectionResult{snapshot: snapshot, skipped: l.skipped.Load() > before, diagnostic: diagnostic}
	if err == nil && !budgetExceeded && l.ctx.Err() == nil {
		result.sample = current
		result.commitSample = true
	}
	return result
}

func classifyResourceOutcome(snapshot coreresources.Snapshot, err error, started time.Time) resourceDiagnosticOutcome {
	if err != nil {
		source, failure := diagnostics.ResourceSourceSampler, diagnostics.ResourceFailureCollection
		var classified *resourceCollectionError
		if errors.As(err, &classified) {
			source, failure = classified.source, classified.failure
		}
		outcome := resourceDiagnosticOutcome{started: started, source: source, result: diagnostics.ResourceResultError, failure: failure, record: true}
		switch source {
		case diagnostics.ResourceSourceProjectDiscovery:
			outcome.recover = []diagnostics.ResourceSource{diagnostics.ResourceSourceInventory}
		case diagnostics.ResourceSourceSampler:
			outcome.recover = []diagnostics.ResourceSource{diagnostics.ResourceSourceInventory, diagnostics.ResourceSourceProjectDiscovery}
		}
		return outcome
	}
	outcome := resourceDiagnosticOutcome{
		started: started,
		recover: []diagnostics.ResourceSource{diagnostics.ResourceSourceInventory, diagnostics.ResourceSourceProjectDiscovery, diagnostics.ResourceSourceRefresh},
	}
	switch snapshot.Status {
	case coreresources.StatusUnavailable:
		outcome.source, outcome.result, outcome.failure, outcome.record = diagnostics.ResourceSourceSampler, diagnostics.ResourceResultUnavailable, diagnostics.ResourceFailureSampleUnavailable, true
	case coreresources.StatusPartial:
		outcome.source, outcome.result, outcome.failure, outcome.record = diagnostics.ResourceSourceSampler, diagnostics.ResourceResultPartial, diagnostics.ResourceFailureSamplePartial, true
	default:
		outcome.healthy = true
	}
	return outcome
}

func (l *resourceLifecycle) finishCollection(result resourceCollectionResult, deferred bool) {
	l.mu.Lock()
	l.collecting = false
	if result.commitSample {
		l.previous = &result.sample
	}
	if result.snapshot.At.IsZero() {
		l.lastCompleteAt = l.now()
	} else {
		l.lastCompleteAt = result.snapshot.At
	}
	if deferred && !l.closing && l.ctx.Err() == nil {
		l.completion = &result
	}
	shouldNotify := deferred && l.completion != nil
	l.mu.Unlock()
	if l.afterStateCommit != nil {
		l.afterStateCommit()
	}
	l.applyResourceOutcome(result.diagnostic)
	l.active.Done()
	if shouldNotify {
		l.notify()
	}
}

func (l *resourceLifecycle) applyResourceOutcome(outcome resourceDiagnosticOutcome) {
	l.diagnosticsMu.Lock()
	defer l.diagnosticsMu.Unlock()
	if outcome.healthy {
		l.diagnostics.Healthy()
		return
	}
	if len(outcome.recover) > 0 {
		l.diagnostics.Recover(outcome.recover...)
	}
	if outcome.record {
		l.diagnostics.Record(outcome.source, outcome.result, outcome.failure, outcome.started)
	}
}

func (l *resourceLifecycle) requestAutomatic() {
	l.mu.Lock()
	if l.closing {
		l.mu.Unlock()
		return
	}
	staleStarted := l.lastCompleteAt
	stale := !staleStarted.IsZero() && l.now().Sub(staleStarted) > 2*l.interval
	if l.collecting || l.automaticPending || l.manualPending {
		l.skipped.Add(1)
		l.mu.Unlock()
		if stale {
			l.recordStaleIfCurrent(staleStarted)
		}
		return
	}
	l.automaticPending = true
	l.mu.Unlock()
	if stale {
		l.recordStaleIfCurrent(staleStarted)
	}
	l.notify()
}

func (l *resourceLifecycle) request(view *resourceViewState) {
	view.setRefreshing(true)
	l.mu.Lock()
	if l.closing {
		l.mu.Unlock()
		return
	}
	staleStarted := l.lastCompleteAt
	stale := !staleStarted.IsZero() && l.now().Sub(staleStarted) > 2*l.interval
	if l.collecting || l.manualPending {
		l.skipped.Add(1)
		l.mu.Unlock()
		if stale {
			l.recordStaleIfCurrent(staleStarted)
		}
		view.setSkippedRefresh(true)
		return
	}
	l.automaticPending = false
	l.manualPending = true
	l.mu.Unlock()
	if stale {
		l.recordStaleIfCurrent(staleStarted)
	}
	l.notify()
}

func (l *resourceLifecycle) recordStaleIfCurrent(started time.Time) {
	l.diagnosticsMu.Lock()
	defer l.diagnosticsMu.Unlock()
	l.mu.Lock()
	stale := !l.closing && l.lastCompleteAt.Equal(started) && l.now().Sub(started) > 2*l.interval
	l.mu.Unlock()
	if stale {
		l.diagnostics.Record(diagnostics.ResourceSourceRefresh, diagnostics.ResourceResultStale, diagnostics.ResourceFailureSampleStale, started)
	}
}

func (l *resourceLifecycle) notify() {
	select {
	case l.trigger <- struct{}{}:
	default:
		l.skipped.Add(1)
	}
}

func (l *resourceLifecycle) close() {
	l.mu.Lock()
	l.closing = true
	l.mu.Unlock()
	l.cancel()
	<-l.done
	l.active.Wait()
	l.mu.Lock()
	l.previous = nil
	l.lastCompleteAt = time.Time{}
	l.completion = nil
	l.automaticPending = false
	l.manualPending = false
	l.mu.Unlock()
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
	kind        resourceScopeKind
	projectKey  string
	socket      string
	windowID    string
	paneID      string
	paneDisplay string
}

type resourceViewState struct {
	mu         sync.RWMutex
	now        func() time.Time
	scope      resourceScope
	sort       resourceSort
	snap       coreresources.Snapshot
	skipped    bool
	refreshing bool
	feedback   string
	text       resourceText
	palette    resourceSemanticPalette
	order      []string
	ordered    resourceScope
	reorder    bool
}

func newResourceViewState(now func() time.Time, locale i18n.Locale) *resourceViewState {
	return &resourceViewState{now: now, sort: resourceSortName, reorder: true, text: resourceText{locale: locale}, snap: coreresources.Snapshot{Status: coreresources.StatusWarming, StatusReason: "first-sample-pending"}}
}

func (v *resourceViewState) setSnapshot(snapshot coreresources.Snapshot, skipped bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.snap = snapshot
	v.skipped = skipped
	v.refreshing = false
}

func (v *resourceViewState) setRefreshing(refreshing bool) {
	v.mu.Lock()
	v.refreshing = refreshing
	v.mu.Unlock()
}

func (v *resourceViewState) atRoot() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.scope.kind == resourceScopeProjects
}

func (v *resourceViewState) setSkippedRefresh(skipped bool) {
	v.mu.Lock()
	v.skipped = skipped
	v.mu.Unlock()
}

func (v *resourceViewState) setPalette(palette resourceSemanticPalette) {
	v.mu.Lock()
	v.palette = palette
	v.mu.Unlock()
}

func (v *resourceViewState) cycleSort() {
	v.mu.Lock()
	v.sort = (v.sort + 1) % 3
	v.feedback = ""
	v.reorder = true
	v.mu.Unlock()
}

func (v *resourceViewState) back() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	switch v.scope.kind {
	case resourceScopePaneDetail:
		v.scope.kind = resourceScopePanes
		v.scope.paneID = ""
		v.scope.paneDisplay = ""
	case resourceScopePanes:
		v.scope.kind = resourceScopeWindows
		v.scope.windowID = ""
		v.scope.paneID = ""
		v.scope.paneDisplay = ""
	case resourceScopeWindows:
		v.scope = resourceScope{}
	default:
		return false
	}
	v.feedback = ""
	v.reorder = true
	return true
}

func (v *resourceViewState) enter(value string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.feedback = ""
	switch v.scope.kind {
	case resourceScopeProjects:
		if value == "project:"+coreresources.OtherUnattributed || value == "project:"+coreresources.ProjectShared || !strings.HasPrefix(value, "project:") {
			return
		}
		v.scope = resourceScope{kind: resourceScopeWindows, projectKey: strings.TrimPrefix(value, "project:")}
		v.reorder = true
	case resourceScopeWindows:
		socket, id, ok := parseResourceStableValue(value, "window:")
		if !ok {
			return
		}
		v.scope.kind, v.scope.socket, v.scope.windowID = resourceScopePanes, socket, id
		v.scope.paneID, v.scope.paneDisplay = "", ""
		v.reorder = true
	case resourceScopePanes:
		socket, id, ok := parseResourceStableValue(value, "pane:")
		if !ok {
			return
		}
		display := v.text.value("picker.resources.unnamed_pane", "Unnamed pane")
		if pane, found := resourcePane(v.snap, socket, id); found {
			if resolved := resourcePaneDisplayIdentity(pane, v.text); resolved != "" {
				display = resolved
			}
		}
		v.scope.kind, v.scope.socket, v.scope.paneID, v.scope.paneDisplay = resourceScopePaneDetail, socket, id, display
		v.reorder = true
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
	return intpicker.DeferredUpdate{Items: screen.items, ResourceSummaryDock: screen.bands, SetResourceSummaryDock: true, Footer: screen.footer, SetFooter: true, Title: screen.title, SetTitle: true, DisableSearch: !screen.actionable, ReadOnly: !screen.actionable, SetInteraction: true}
}

type resourceScreen struct {
	title      string
	bands      []intpicker.ChromeBand
	items      []intpicker.Item
	footer     string
	actionable bool
}

func (v *resourceViewState) screen() resourceScreen {
	v.mu.Lock()
	defer v.mu.Unlock()
	items := v.itemsLocked()
	actionable := v.scope.kind != resourceScopePaneDetail && len(items) > 0 && v.snap.Status != coreresources.StatusWarming && v.snap.Status != coreresources.StatusUnavailable
	return resourceScreen{
		title:      resourceBreadcrumb(v.snap, v.scope, v.text),
		bands:      resourceSummaryBands(v.snap, v.currentTime(), v.skipped, v.refreshing, v.scope, v.palette, v.text),
		items:      items,
		footer:     resourceFooter(v.snap, v.currentTime(), v.skipped, v.refreshing, v.scope.kind, v.sort, actionable, v.feedback, v.text),
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
	countKind                        resourceCountKind
	disabled                         bool
	meta                             []string
}

type resourceCountKind uint8

const (
	resourceCountPanes resourceCountKind = iota
	resourceCountProcesses
)

type resourceSemanticPalette struct {
	normal, unknown, warning, critical string
}

func newResourceSemanticPalette(effective theme.EffectiveTheme) resourceSemanticPalette {
	roles := theme.ANSIRolesFromEffective(effective)
	return resourceSemanticPalette{
		normal:   roles.TextSecondary,
		unknown:  roles.TextMuted,
		warning:  roles.StateWarning,
		critical: roles.StateCritical + theme.ANSIBold,
	}
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
		rows = resourcePaneDetailRows(v.snap, v.scope.socket, v.scope.paneID, v.palette, v.text)
	}
	if v.scope.kind != resourceScopePaneDetail {
		v.orderRowsLocked(rows)
	}
	items := make([]intpicker.Item, 0, len(rows))
	for _, row := range rows {
		label := row.identity
		if row.disabled {
			label = v.palette.unknown + "·  " + label + theme.ANSIReset
		}
		meta := append([]string(nil), row.meta...)
		if v.scope.kind != resourceScopePaneDetail {
			count := "--"
			metricsKey, metricsFallback := i18n.Key("picker.resources.row.metrics.panes"), "CPU {cpu}  MEMORY {memory}  PANES {count}"
			if row.countKind == resourceCountProcesses {
				metricsKey, metricsFallback = "picker.resources.row.metrics.processes", "CPU {cpu}  MEMORY {memory}  PROCESSES {count}"
			}
			if row.countKnown {
				count = fmt.Sprint(row.count)
			}
			if row.context != "" {
				meta = append([]string{row.context}, meta...)
			}
			meta = append(meta, v.text.format(metricsKey, metricsFallback,
				"{cpu}", projmuxpicker.PadRight(formatResourceCPUState(row.cpu, v.palette, v.text), 18),
				"{memory}", projmuxpicker.PadRight(formatResourceMemoryState(row.rss, row.memPercent, row.memKnown, v.palette, v.text), 32),
				"{count}", count,
			))
		}
		items = append(items, intpicker.Item{Label: label, Title: label, Value: row.value, SearchText: row.search, MetaLines: meta})
	}
	return items
}

func (v *resourceViewState) orderRowsLocked(rows []resourceRow) {
	if v.reorder || v.ordered != v.scope || len(v.order) == 0 {
		sortResourceRows(rows, v.sort)
		v.order = make([]string, 0, len(rows))
		for _, row := range rows {
			v.order = append(v.order, row.value)
		}
		v.ordered, v.reorder = v.scope, false
		return
	}
	rank := make(map[string]int, len(v.order))
	for index, value := range v.order {
		rank[value] = index
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, aOK := rank[rows[i].value]
		b, bOK := rank[rows[j].value]
		if aOK != bOK {
			return aOK
		}
		if aOK {
			return a < b
		}
		return strings.ToLower(rows[i].identity) < strings.ToLower(rows[j].identity)
	})
	v.order = v.order[:0]
	for _, row := range rows {
		v.order = append(v.order, row.value)
	}
}

func resourceProjectRows(snapshot coreresources.Snapshot, text resourceText) []resourceRow {
	byKey := make(map[string]coreresources.ProjectUsage, len(snapshot.Projects))
	for _, project := range snapshot.Projects {
		byKey[project.Key] = project
	}
	keys := []string{coreresources.ProjectUnassigned}
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
		if key != coreresources.ProjectUnassigned {
			identity = filepath.Base(key)
			context = text.format("picker.resources.row.path", "Path  {path}", "{path}", key)
		} else {
			context = text.value("picker.resources.bucket.unassigned_help", "Pane path matches no managed project root.")
		}
		rows = append(rows, resourceRow{identity: identity, context: context, value: "project:" + key, search: identity + " " + key, cpu: project.CPU, rss: project.Memory.RSSBytes, memPercent: project.Memory.HostPercent, memKnown: ok && snapshot.Host.MemoryAvailable, count: project.PaneCount, countKnown: ok && snapshot.Status != coreresources.StatusUnavailable})
	}
	return rows
}

func resourceProjectDisplayName(key string, text resourceText) string {
	switch key {
	case coreresources.ProjectUnassigned:
		return text.value("picker.resources.bucket.unassigned", "No project match")
	case coreresources.ProjectShared:
		return text.value("picker.resources.diagnostic.ambiguous_label", "Ambiguous attribution")
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
		secondary := text.format("picker.resources.context.sessions", "Session  {sessions}", "{sessions}", context)
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
		identity := resourcePaneDisplayIdentity(pane, text)
		secondary := resourcePaneContext(pane, text)
		rows = append(rows, resourceRow{identity: identity, context: secondary, value: "pane:" + pane.Socket + "\x1f" + pane.PaneID, search: identity + " " + secondary + " " + pane.PaneID, cpu: pane.CPU, rss: pane.Memory.RSSBytes, memPercent: pane.Memory.HostPercent, memKnown: snapshot.Host.MemoryAvailable, count: pane.ProcessCount, countKnown: true, countKind: resourceCountProcesses})
	}
	return rows
}

func resourcePaneDetailRows(snapshot coreresources.Snapshot, socket, paneID string, palette resourceSemanticPalette, text resourceText) []resourceRow {
	for _, pane := range snapshot.Panes {
		if pane.Socket != socket || pane.PaneID != paneID {
			continue
		}
		cpu := formatResourceCPUState(pane.CPU, palette, text)
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
			resourcePaneContext(pane, text),
			text.format("picker.resources.detail.processes_cpu", "Processes: {count}  CPU host share: {cpu}", "{count}", fmt.Sprint(pane.ProcessCount), "{cpu}", cpu),
			text.format("picker.resources.detail.memory", "Memory RSS sum: {memory}", "{memory}", formatResourceMemoryState(pane.Memory.RSSBytes, pane.Memory.HostPercent, snapshot.Host.MemoryAvailable, palette, text)),
			text.value("picker.resources.detail.rss_caveat", "RSS sum may count shared pages more than once."),
		}
		return []resourceRow{{identity: resourcePaneDisplayIdentity(pane, text), value: "", meta: meta}}
	}
	return []resourceRow{{identity: text.value("picker.resources.detail.gone", "Pane no longer exists"), value: "", meta: []string{text.value("picker.resources.detail.gone_help", "The selected pane vanished during refresh. Left returns to the nearest available pane.")}}}
}

func resourcePaneContext(pane coreresources.PaneUsage, text resourceText) string {
	command := strings.TrimSpace(pane.PaneCommand)
	if command == "" {
		command = "--"
	}
	return text.format("picker.resources.context.pane", "Process  {process} · PID/SID  {pid} · Pane  {pane} · TTY  {tty}",
		"{process}", command,
		"{pid}", fmt.Sprint(pane.PanePID),
		"{pane}", pane.PaneID,
		"{tty}", pane.PaneTTY,
	)
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
			segments = append(segments, resourcePaneDisplayIdentity(pane, text))
		} else {
			identity := strings.TrimSpace(scope.paneDisplay)
			if identity == "" {
				identity = text.value("picker.resources.unnamed_pane", "Unnamed pane")
			}
			segments = append(segments, identity)
		}
	}
	if len(segments) == 0 {
		return title
	}
	return title + " / " + strings.Join(segments, " / ")
}

func resourcePaneDisplayIdentity(pane coreresources.PaneUsage, text resourceText) string {
	identity := paneidentity.Resolve(paneidentity.Inputs{Label: pane.PaneLabel, AIAgent: pane.AIAgent, AITopic: pane.AITopic, Command: pane.PaneCommand, Title: pane.PaneTitle}).Value
	if identity == "" {
		return text.value("picker.resources.unnamed_pane", "Unnamed pane")
	}
	return identity
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

func resourceSummaryBands(snapshot coreresources.Snapshot, now time.Time, skipped, refreshing bool, scope resourceScope, palette resourceSemanticPalette, text resourceText) []intpicker.ChromeBand {
	status := text.status(snapshot.Status)
	if skipped && snapshot.Status != coreresources.StatusUnavailable {
		status = text.value("picker.resources.status.refresh_skipped", "refresh delayed · showing last complete sample")
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
	age, stale := resourceSampleAge(snapshot, now)
	if snapshot.Status == coreresources.StatusWarming {
		status = text.value("picker.resources.status.collecting", "warming · collecting first sample")
	} else if refreshing {
		status = text.value("picker.resources.status.refreshing", "refresh in progress · last complete sample · not fresh")
	} else if snapshot.Status == coreresources.StatusReady && stale {
		status = text.value("picker.resources.status.stale", "stale · last complete state ready")
	} else if snapshot.Status == coreresources.StatusReady {
		status += " · " + text.value("picker.resources.status.fresh", "fresh")
	}
	bands := []intpicker.ChromeBand{
		{Label: text.value("picker.resources.band.host", "Host"), Value: text.format("picker.resources.band.host.metrics", "CPU {cpu}    Memory {memory}", "{cpu}", formatResourcePercentState(snapshot.CPU.HostBusyPercent, liveResourceCPUWarningAt, palette, text), "{memory}", formatResourcePercentState(hostMem, liveResourceMemoryWarningAt, palette, text))},
		{Label: text.value("picker.resources.band.attributed", "Attributed"), Value: text.format("picker.resources.band.attributed.metrics", "CPU {cpu}    RSS {rss} ({memory})", "{cpu}", formatResourcePercentState(snapshot.CPU.AttributedPercent, liveResourceCPUWarningAt, palette, text), "{rss}", attrRSS, "{memory}", formatResourcePercentState(attrMem, liveResourceMemoryWarningAt, palette, text))},
	}
	coverage := intpicker.ChromeBand{Label: text.value("picker.resources.band.coverage", "Coverage"), Value: text.value("picker.resources.coverage.current_scope", "Current scope has resource rows")}
	if scope.kind == resourceScopeProjects {
		otherCPU := formatResourceUnknown()
		otherMemory := formatResourceUnknown()
		if snapshot.Other.CPUHostSharePercent != nil {
			otherCPU = formatResourcePercent(snapshot.Other.CPUHostSharePercent)
		}
		if snapshot.Other.MemoryBytes != nil {
			otherMemory = formatBytes(*snapshot.Other.MemoryBytes)
		}
		coverage.Value = resourceProjectDisplayName(coreresources.OtherUnattributed, text) + "  " + text.format("picker.resources.band.other.metrics", "CPU {cpu}    Memory {memory}", "{cpu}", otherCPU, "{memory}", otherMemory)
		coverage.Secondary = text.value("picker.resources.other_not_drillable", "summary only · not drillable")
	}
	if scope.kind == resourceScopeWindows && len(resourceWindowRows(snapshot, scope.projectKey, text)) == 0 {
		coverage.Value = text.value("picker.resources.empty.windows", "No windows in this project")
		coverage.Secondary = text.value("picker.resources.empty.not_actionable", "No row to open")
	}
	if scope.kind == resourceScopePanes && len(resourcePaneRows(snapshot, scope.socket, scope.windowID, text)) == 0 {
		coverage.Value = text.value("picker.resources.empty.panes", "No panes in this window")
		coverage.Secondary = text.value("picker.resources.empty.not_actionable", "No row to open")
	}
	if scope.kind == resourceScopePaneDetail {
		if _, ok := resourcePane(snapshot, scope.socket, scope.paneID); !ok {
			coverage.Value = text.value("picker.resources.detail.gone", "Pane no longer exists")
			coverage.Secondary = text.value("picker.resources.detail.gone_bounded", "The latest complete sample no longer contains this pane.")
		}
	}
	bands = append(bands, coverage)
	bands = append(bands, intpicker.ChromeBand{
		Label:     text.value("picker.resources.band.sample", "Sample"),
		Value:     text.format("picker.resources.band.sample.metrics", "age {age} · {status}", "{age}", age, "{status}", status),
		Secondary: resourceDiagnostic(snapshot, text),
	})
	return bands
}

func resourceFooter(snapshot coreresources.Snapshot, now time.Time, skipped, refreshing bool, scope resourceScopeKind, order resourceSort, actionable bool, actionFeedback string, text resourceText) string {
	sorts := []string{
		text.value("picker.resources.sort.cpu", "CPU"),
		text.value("picker.resources.sort.memory", "Memory"),
		text.value("picker.resources.sort.name", "Name"),
	}
	var actions string
	if scope == resourceScopePaneDetail {
		actions = text.value("picker.resources.footer.detail", "Read-only pane detail | Left: back | Esc: close | Ctrl-R: refresh")
	} else if !actionable {
		if scope == resourceScopeProjects {
			actions = text.value("picker.resources.footer.root_readonly", "Read-only | Esc: close | Ctrl-R: refresh")
		} else {
			actions = text.value("picker.resources.footer.empty", "Read-only | Left: back | Esc: close | Ctrl-R: refresh")
		}
	} else if scope == resourceScopeProjects {
		actions = text.format("picker.resources.footer.root", "Right/Enter: drill down | Esc: close | Tab: sort {sort} | Ctrl-R: refresh", "{sort}", sorts[order])
	} else {
		actions = text.format("picker.resources.footer.list", "Right/Enter: drill down | Left: back | Esc: close | Tab: sort {sort} | Ctrl-R: refresh", "{sort}", sorts[order])
	}
	if actionFeedback = strings.TrimSpace(actionFeedback); actionFeedback != "" {
		actions = actionFeedback + " | " + actions
	}
	age, stale := resourceSampleAge(snapshot, now)
	feedback := text.value("picker.resources.refresh.automatic", "automatic every 2s")
	if refreshing {
		feedback = text.value("picker.resources.refresh.in_progress", "in progress; last complete sample retained")
	} else if snapshot.Status == coreresources.StatusWarming {
		feedback = text.value("picker.resources.refresh.collecting", "collecting first sample")
	} else if skipped {
		feedback = text.value("picker.resources.refresh.delayed", "delayed; last complete sample retained")
	} else if stale {
		feedback = text.value("picker.resources.refresh.stale", "stale; waiting for a complete sample")
	}
	return actions + "\n" + text.format("picker.resources.footer.refresh", "Refresh: {feedback} | sample age {age}", "{feedback}", feedback, "{age}", age)
}

func resourceSampleAge(snapshot coreresources.Snapshot, now time.Time) (string, bool) {
	if snapshot.At.IsZero() {
		return "--", false
	}
	d := max(now.Sub(snapshot.At), 0)
	return d.Round(100 * time.Millisecond).String(), d > 2*resourceRefreshInterval
}

func resourceDiagnostic(snapshot coreresources.Snapshot, text resourceText) string {
	var diagnostics []string
	if shared, ok := resourceSharedProjectUsage(snapshot); ok {
		cpu := formatResourceUnknown()
		if shared.CPU != nil {
			cpu = formatResourcePercent(&shared.CPU.HostSharePercent)
		}
		diagnostics = append(diagnostics, text.format("picker.resources.diagnostic.ambiguous", "Ambiguous attribution retained: CPU {cpu} · RSS {rss} · panes {panes}; included in Attributed totals, not drillable",
			"{cpu}", cpu,
			"{rss}", formatBytes(shared.Memory.RSSBytes),
			"{panes}", fmt.Sprint(shared.PaneCount),
		))
	}
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

func resourceSharedProjectUsage(snapshot coreresources.Snapshot) (coreresources.ProjectUsage, bool) {
	for _, project := range snapshot.Projects {
		if project.Key == coreresources.ProjectShared && (project.PaneCount > 0 || project.WindowCount > 0 || project.ProcessCount > 0 || project.CPU != nil || project.Memory.RSSBytes > 0) {
			return project, true
		}
	}
	return coreresources.ProjectUsage{}, false
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

func formatResourceCPUState(cpu *coreresources.CPUUsage, palette resourceSemanticPalette, text resourceText) string {
	if cpu == nil {
		return formatResourceMetric("--", nil, liveResourceCPUWarningAt, palette, text)
	}
	percent := cpu.HostSharePercent
	return formatResourceMetric(fmt.Sprintf("%.1f%%", percent), &percent, liveResourceCPUWarningAt, palette, text)
}

func formatResourceMemoryState(bytes uint64, percent *float64, known bool, palette resourceSemanticPalette, text resourceText) string {
	if !known || percent == nil {
		return formatResourceMetric("--", nil, liveResourceMemoryWarningAt, palette, text)
	}
	return formatResourceMetric(fmt.Sprintf("%s (%s)", formatBytes(bytes), formatResourcePercent(percent)), percent, liveResourceMemoryWarningAt, palette, text)
}

func formatResourcePercentState(percent *float64, warningAt int, palette resourceSemanticPalette, text resourceText) string {
	if percent == nil {
		return formatResourceMetric("--", nil, warningAt, palette, text)
	}
	return formatResourceMetric(formatResourcePercent(percent), percent, warningAt, palette, text)
}

func formatResourceUnknown() string {
	return "--"
}

func formatResourceMetric(value string, percent *float64, warningAt int, palette resourceSemanticPalette, text resourceText) string {
	severity := liveResourceUnknown
	if percent != nil {
		severity = classifyResourcePercent(*percent, float64(warningAt))
	}
	style := palette.unknown
	switch severity {
	case liveResourceNormal:
		style = palette.normal
	case liveResourceWarning:
		style = palette.warning
	case liveResourceCritical:
		style = palette.critical
	}
	if style == "" {
		return value
	}
	return style + value + theme.ANSIReset
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
