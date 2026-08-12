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
	items, header, footer := view.render()
	text := view.text
	actions := pickerCloseActionsForPopupToggleMode(c.homeDir, c.lookupEnv, resourceInspectorPopupMode, "esc")
	actions = append(actions,
		intpicker.Action{Key: "tab", Intent: intpicker.ActionCustom, Mutate: func(intpicker.ActionContext) (intpicker.DeferredUpdate, error) {
			view.cycleSort()
			return view.deferredUpdate(), nil
		}},
		intpicker.Action{Key: "ctrl-r", Intent: intpicker.ActionCustom, Mutate: func(intpicker.ActionContext) (intpicker.DeferredUpdate, error) {
			return lifecycle.collect(view), nil
		}},
		intpicker.Action{Key: "alt-left", Intent: intpicker.ActionCustom, Mutate: func(ctx intpicker.ActionContext) (intpicker.DeferredUpdate, error) {
			return intpicker.DeferredUpdate{Result: &intpicker.Result{Key: "back", Query: ctx.Query, Closed: true}}, nil
		}},
	)
	options := intpicker.Options{
		UI:                    "resources",
		Title:                 text.value(i18n.KeyPickerResourcesTitle, "Resources"),
		Prompt:                text.value(i18n.KeyPickerResourcesPrompt, "search › "),
		Header:                header,
		Footer:                footer,
		Items:                 items,
		MultiLine:             true,
		Locale:                text.locale,
		Actions:               actions,
		DeferredUpdate:        func() (intpicker.DeferredUpdate, error) { return lifecycle.collect(view), nil },
		DeferredUpdateTrigger: lifecycle.trigger,
	}
	if source, err := configRenderThemeSource(c.homeDir, c.lookupEnv, ""); err == nil {
		return source.pickerOptions(options)
	}
	return fallbackRenderThemeSource().pickerOptions(options)
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
	items, header, footer := v.render()
	return intpicker.DeferredUpdate{Items: items, Header: header, Footer: footer, SetHeader: true, SetFooter: true}
}

func (v *resourceViewState) render() ([]intpicker.Item, string, string) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	items := v.itemsLocked()
	return items, resourceHeader(v.snap, v.currentTime(), v.skipped, v.text), resourceFooter(v.scope.kind, v.sort, v.snap, v.text)
}

func (v *resourceViewState) currentTime() time.Time {
	if v.now == nil {
		return time.Now()
	}
	return v.now()
}

type resourceRow struct {
	label, value, search string
	cpu                  *coreresources.CPUUsage
	rss                  uint64
	memPercent           *float64
	memKnown             bool
	count                int
	countKnown           bool
	meta                 []string
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
		label := row.label
		if v.scope.kind != resourceScopePaneDetail {
			paneCount := v.text.value("picker.resources.row.pane_count_unknown", "-- panes")
			if row.countKnown {
				paneCount = v.text.format("picker.resources.row.pane_count", "{count} panes", "{count}", fmt.Sprint(row.count))
			}
			label = v.text.format("picker.resources.row.metrics", "{label}  CPU {cpu}  RSS sum {memory}  {panes}",
				"{label}", label,
				"{cpu}", formatResourceCPU(row.cpu),
				"{memory}", formatResourceMemory(row.rss, row.memPercent, row.memKnown),
				"{panes}", paneCount,
			)
		}
		items = append(items, intpicker.Item{Label: label, Title: label, Value: row.value, SearchText: row.search, MetaLines: row.meta})
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
	rows := make([]resourceRow, 0, len(keys)+1)
	for _, key := range keys {
		project, ok := byKey[key]
		label := resourceProjectDisplayName(key, text)
		if key != coreresources.ProjectUnassigned && key != coreresources.ProjectShared {
			label = filepath.Base(key) + "  " + key
		}
		rows = append(rows, resourceRow{label: label, value: "project:" + key, search: label + " " + key, cpu: project.CPU, rss: project.Memory.RSSBytes, memPercent: project.Memory.HostPercent, memKnown: ok && snapshot.Host.MemoryAvailable, count: project.PaneCount, countKnown: ok && snapshot.Status != coreresources.StatusUnavailable})
	}
	otherCPU := (*coreresources.CPUUsage)(nil)
	if snapshot.Other.CPUHostSharePercent != nil {
		otherCPU = &coreresources.CPUUsage{HostSharePercent: *snapshot.Other.CPUHostSharePercent}
	}
	otherRSS, otherKnown := uint64(0), snapshot.Other.MemoryBytes != nil
	if otherKnown {
		otherRSS = *snapshot.Other.MemoryBytes
	}
	otherLabel := resourceProjectDisplayName(coreresources.OtherUnattributed, text)
	rows = append(rows, resourceRow{label: text.format("picker.resources.other_not_drillable", "{label} (not drillable)", "{label}", otherLabel), value: "project:" + coreresources.OtherUnattributed, search: otherLabel + " " + coreresources.OtherUnattributed, cpu: otherCPU, rss: otherRSS, memKnown: otherKnown})
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
		label := strings.TrimSpace(context + " / " + window.WindowName + "  " + window.WindowID)
		rows = append(rows, resourceRow{label: label, value: "window:" + window.Socket + "\x1f" + window.WindowID, search: label + " " + window.WindowID, cpu: window.CPU, rss: window.Memory.RSSBytes, memPercent: window.Memory.HostPercent, memKnown: snapshot.Host.MemoryAvailable, count: window.PaneCount, countKnown: true})
	}
	if len(rows) == 0 {
		label := text.value("picker.resources.empty.windows", "No windows in this project")
		rows = append(rows, resourceRow{label: label, value: "empty:windows", search: label + " empty no windows"})
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
		label := identity + "  " + pane.PaneID
		rows = append(rows, resourceRow{label: label, value: "pane:" + pane.Socket + "\x1f" + pane.PaneID, search: label + " " + pane.PaneID, cpu: pane.CPU, rss: pane.Memory.RSSBytes, memPercent: pane.Memory.HostPercent, memKnown: snapshot.Host.MemoryAvailable, count: 1, countKnown: true})
	}
	if len(rows) == 0 {
		label := text.value("picker.resources.empty.panes", "No panes in this window")
		rows = append(rows, resourceRow{label: label, value: "empty:panes", search: label + " empty no panes"})
	}
	return rows
}

func resourcePaneDetailRows(snapshot coreresources.Snapshot, socket, paneID string, text resourceText) []resourceRow {
	for _, pane := range snapshot.Panes {
		if pane.Socket != socket || pane.PaneID != paneID {
			continue
		}
		identity := paneidentity.Resolve(paneidentity.Inputs{Label: pane.PaneLabel, AIAgent: pane.AIAgent, AITopic: pane.AITopic, Command: pane.PaneCommand, Title: pane.PaneTitle}).Value
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
		return []resourceRow{{label: identity + "  " + pane.PaneID, value: "detail:" + pane.Socket + "\x1f" + pane.PaneID, search: identity + " " + pane.PaneID, meta: meta}}
	}
	return []resourceRow{{label: text.value("picker.resources.detail.gone", "Pane no longer exists"), value: "detail:gone", meta: []string{text.value("picker.resources.detail.gone_help", "The selected pane vanished during refresh. Esc returns to the nearest available pane.")}}}
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
			if !strings.EqualFold(a.label, b.label) {
				return strings.ToLower(a.label) < strings.ToLower(b.label)
			}
		}
		return a.value < b.value
	})
}

func resourceHeader(snapshot coreresources.Snapshot, now time.Time, skipped bool, text resourceText) string {
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
	return text.format("picker.resources.header", "Host CPU {host_cpu} MEM {host_mem} | Attributed CPU {attributed_cpu} RSS sum {attributed_rss} ({attributed_mem}) | age {age} | {status}",
		"{host_cpu}", formatResourcePercent(snapshot.CPU.HostBusyPercent),
		"{host_mem}", formatResourcePercent(hostMem),
		"{attributed_cpu}", formatResourcePercent(snapshot.CPU.AttributedPercent),
		"{attributed_rss}", attrRSS,
		"{attributed_mem}", formatResourcePercent(attrMem),
		"{age}", age,
		"{status}", status,
	)
}

func resourceFooter(scope resourceScopeKind, order resourceSort, snapshot coreresources.Snapshot, text resourceText) string {
	sorts := []string{
		text.value("picker.resources.sort.cpu", "CPU"),
		text.value("picker.resources.sort.memory", "Memory"),
		text.value("picker.resources.sort.name", "Name"),
	}
	footer := text.format("picker.resources.footer.list", "Enter: drill down | Esc/Alt-Left: back/close | Tab: sort {sort} | Ctrl-R: refresh", "{sort}", sorts[order])
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
	if scope == resourceScopePaneDetail {
		footer = text.value("picker.resources.footer.detail", "Read-only pane detail | Esc/Alt-Left: back | Ctrl-R: refresh")
	}
	if len(diagnostics) > 0 {
		footer += " | " + strings.Join(diagnostics, " | ")
	}
	return footer
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
