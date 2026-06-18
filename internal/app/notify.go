package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/theme"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// notifyStore is the subset of *notify.Store the CLI dispatcher needs. The
// interface keeps the command layer mockable in tests without leaking file
// paths into the test setup.
type notifyStore interface {
	Push(notify.PushInput) (notify.Notification, notify.PushResult, error)
	List() ([]notify.Notification, error)
	Ack(id string) error
	AckAll() (int, error)
}

type notifyCommand struct {
	store      notifyStore
	storeErr   error
	now        func() time.Time
	runner     tmuxRunner
	hooks      *sendNotiHookDispatcher
	events     notifyQueueRefreshEvents
	picker     intpickercompat.Runner
	native     intpicker.Runner
	executable func() (string, error)
	lookupEnv  func(string) string
	homeDir    func() (string, error)
}

func newNotifyCommand() *notifyCommand {
	cmd := &notifyCommand{
		now:        time.Now,
		runner:     reconcileDefaultRunner(),
		hooks:      newSendNotiHookDispatcher(),
		native:     intpicker.NativeRunner{In: os.Stdin, Out: os.Stdout},
		executable: os.Executable,
		lookupEnv:  os.Getenv,
		homeDir:    os.UserHomeDir,
	}
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		cmd.storeErr = fmt.Errorf("resolve default config paths: %w", err)
		return cmd
	}
	cmd.store = notify.NewDefaultStore(paths)
	cmd.events = newNotifyQueueRefreshTransport(paths.StateDir)
	return cmd
}

// Run dispatches the configured notify subcommands.
func (c *notifyCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printNotifyUsage(stderr)
		return usageError("notify requires a subcommand")
	}

	switch args[0] {
	case "push":
		return c.runPush(args[1:], stdout, stderr)
	case "list":
		return c.runList(args[1:], stdout, stderr)
	case "ack":
		return c.runAck(args[1:], stdout, stderr)
	case "reconcile":
		return c.runReconcile(args[1:], stdout, stderr)
	case "help", "--help", "-h":
		printNotifyUsage(stdout)
		return nil
	default:
		printNotifyUsage(stderr)
		return usageError(fmt.Sprintf("unknown notify subcommand: %s", args[0]))
	}
}

func (c *notifyCommand) requireStore() (notifyStore, error) {
	if c.storeErr != nil {
		return nil, fmt.Errorf("configure notify store: %w", c.storeErr)
	}
	if c.store == nil {
		return nil, errors.New("configure notify store: notify store is not configured")
	}
	return c.store, nil
}

func (c *notifyCommand) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *notifyCommand) locale() i18n.Locale {
	if c == nil {
		return i18n.FallbackLocale
	}
	return appLocale(c.homeDir, c.lookupEnv)
}

// --- push --------------------------------------------------------------------

func (c *notifyCommand) runPush(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("notify push", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		text     = fs.String("text", "", "notification text (required)")
		target   = fs.String("target", "", "SESSION[:WINDOW[.PANE]] target (required)")
		socket   = fs.String("socket", "", "tmux socket name (optional)")
		severity = fs.String("severity", notify.SeverityInfo, "info|warn|critical")
		source   = fs.String("source", notify.SourceExternal, "ai|k8s|git|external")
		ttlSecs  = fs.Int("ttl", int(notify.DefaultTTL/time.Second), "ttl in seconds")
		id       = fs.String("id", "", "stable id for dedupe (optional)")
		asJSON   = fs.Bool("json", false, "emit json instead of human output")
	)

	if err := fs.Parse(args); err != nil {
		return usageError(fmt.Sprintf("parse notify push flags: %v", err))
	}
	if fs.NArg() != 0 {
		printNotifyUsage(stderr)
		return usageError("notify push does not accept positional arguments")
	}
	if strings.TrimSpace(*text) == "" {
		printNotifyUsage(stderr)
		return usageError("notify push requires --text")
	}
	if strings.TrimSpace(*target) == "" {
		printNotifyUsage(stderr)
		return usageError("notify push requires --target")
	}
	if err := notify.ValidateSeverity(*severity); err != nil {
		printNotifyUsage(stderr)
		return usageError(err.Error())
	}
	if err := notify.ValidateSource(*source); err != nil {
		printNotifyUsage(stderr)
		return usageError(err.Error())
	}
	if *ttlSecs <= 0 {
		printNotifyUsage(stderr)
		return usageError("notify push requires positive --ttl")
	}

	parsed, err := notify.ParseTarget(*target)
	if err != nil {
		printNotifyUsage(stderr)
		return usageError(err.Error())
	}
	parsed.Socket = strings.TrimSpace(*socket)

	store, err := c.requireStore()
	if err != nil {
		return err
	}

	in := notify.PushInput{
		ID:       *id,
		Text:     *text,
		Severity: *severity,
		Source:   *source,
		TTL:      time.Duration(*ttlSecs) * time.Second,
		Target:   parsed,
	}
	entry, result, err := store.Push(in)
	if err != nil {
		if isInvalidInputErr(err) {
			return usageError(err.Error())
		}
		return fmt.Errorf("push notification: %w", err)
	}
	if c.hooks != nil {
		c.hooks.Dispatch(entry, notifyHookMeta{
			Type:    strings.TrimSpace(*source),
			Message: strings.TrimSpace(entry.Text),
		})
	}
	c.publishNotifyQueueRefreshBestEffort()

	if *asJSON {
		payload := map[string]any{
			"id":     result.ID,
			"queued": result.QueueLen,
		}
		return writeJSON(stdout, payload)
	}
	_, err = fmt.Fprintf(stdout, "queued %s (%d in queue)\n", entry.ID, result.QueueLen)
	return err
}

// --- list --------------------------------------------------------------------

func (c *notifyCommand) runList(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("notify list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printNotifyListUsage(stderr) }

	var (
		asJSON     = fs.Bool("json", false, "emit json instead of tabular output")
		live       = fs.Bool("live", false, "include live tmux pane attention explanations")
		limit      = fs.Int("limit", 0, "limit number of returned entries (0 = no limit)")
		ui         = fs.String("ui", "table", "table|sidebar")
		clientTTY  = fs.String("client", "", "origin tmux client tty for sidebar focus")
		severities multiFlag
		sources    multiFlag
	)
	fs.Var(&severities, "severity", "filter by severity (repeatable)")
	fs.Var(&sources, "source", "filter by source (repeatable)")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return usageError(fmt.Sprintf("parse notify list flags: %v", err))
	}
	if fs.NArg() != 0 {
		printNotifyUsage(stderr)
		return usageError("notify list does not accept positional arguments")
	}
	if *limit < 0 {
		return usageError("notify list --limit must be >= 0")
	}
	if *ui != "table" && *ui != "sidebar" {
		return usageError("notify list --ui must be table or sidebar")
	}
	if *ui == "sidebar" && *asJSON {
		return usageError("notify list --ui=sidebar cannot be combined with --json")
	}
	if *ui == "sidebar" && *live {
		return usageError("notify list --ui=sidebar cannot be combined with --live")
	}
	for _, s := range severities {
		if err := notify.ValidateSeverity(s); err != nil {
			return usageError(err.Error())
		}
	}
	for _, s := range sources {
		if err := notify.ValidateSource(s); err != nil {
			return usageError(err.Error())
		}
	}

	store, err := c.requireStore()
	if err != nil {
		return err
	}

	locale := c.locale()
	if *ui == "sidebar" {
		return c.runSidebar(store, severities, sources, *limit, stdout, stderr, c.notifyOriginClient(*clientTTY), locale)
	}

	entries, err := store.List()
	if err != nil {
		return fmt.Errorf("list notifications: %w", err)
	}

	entries = filterEntries(entries, severities, sources)
	if *limit > 0 && len(entries) > *limit {
		entries = entries[:*limit]
	}

	if *asJSON {
		if entries == nil {
			entries = []notify.Notification{}
		}
		if *live {
			report, err := c.buildNotifyLiveReportLocale(entries, locale)
			if err != nil {
				return err
			}
			return writeJSON(stdout, report)
		}
		return writeJSON(stdout, entries)
	}
	if *live {
		report, err := c.buildNotifyLiveReportLocale(entries, locale)
		if err != nil {
			return err
		}
		return writeNotifyLiveTable(stdout, report, c.clock(), locale)
	}
	return writeNotifyTable(stdout, entries, c.clock(), locale)
}

func (c *notifyCommand) runSidebar(store notifyStore, severities, sources []string, limit int, stdout, stderr io.Writer, clientTTY string, locale i18n.Locale) error {
	if c.native == nil {
		return errors.New("native picker is not configured")
	}

	entries, err := c.notifySidebarFilteredEntries(store, severities, sources, limit)
	if err != nil {
		return err
	}
	options := c.notifySidebarPickerOptions(store, entries, severities, sources, limit, locale)
	if trigger, cancel := c.subscribeNotifyQueueRefreshBestEffort(context.Background()); trigger != nil {
		defer cancel()
		options.DeferredUpdate = func() (intpicker.DeferredUpdate, error) {
			return c.notifySidebarDeferredUpdate(store, severities, sources, limit, locale)
		}
		options.DeferredUpdateTrigger = trigger
	}
	if options.Theme == nil {
		options = fallbackRenderThemeSource().pickerOptions(options)
	}
	result, err := c.native.Run(options)
	if err != nil {
		return fmt.Errorf("run notify sidebar: %w", err)
	}
	id := strings.TrimSpace(result.Value)
	if id == "" || id == notifySidebarEmptyValue {
		return nil
	}

	switch {
	case pickerKeyMatchesAction(c.homeDir, c.lookupEnv, result.Key, "NotifySidebar:ClearAll", "ctrl-x"):
		removed, err := store.AckAll()
		if err != nil {
			return fmt.Errorf("clear all notifications: %w", err)
		}
		_, err = fmt.Fprintf(stdout, "cleared %s\n", i18n.FormatCount(removed, i18n.CountNotifications, locale, i18n.FormatFull))
		return err
	case pickerKeyMatchesAction(c.homeDir, c.lookupEnv, result.Key, "NotifySidebar:Ack", "a"):
		if err := store.Ack(id); err != nil {
			return fmt.Errorf("ack notification: %w", err)
		}
		return nil
	case pickerKeyMatchesAction(c.homeDir, c.lookupEnv, result.Key, "NotifySidebar:ClearNonCritical", "x"):
		if err := ackNonCriticalNotifications(store, entries); err != nil {
			return err
		}
		return nil
	default:
		entry, ok := findNotificationByID(entries, id)
		if !ok {
			return fmt.Errorf("focus notification: %w: %s", notify.ErrNotFound, id)
		}
		if err := c.focusNotification(entry, "notify-sidebar", "row-select", clientTTY); err != nil {
			if isFocusTargetUnresolved(err) {
				if ackErr := store.Ack(id); ackErr != nil {
					return fmt.Errorf("ack target-gone notification: %w", ackErr)
				}
				return nil
			}
			return err
		}
		if err := ackFocusedNotification(store, entry, entries); err != nil {
			return fmt.Errorf("ack focused notification: %w", err)
		}
		return nil
	}
}

func (c *notifyCommand) publishNotifyQueueRefreshBestEffort() {
	if c == nil || c.events == nil {
		return
	}
	_ = c.events.Publish()
}

func (c *notifyCommand) subscribeNotifyQueueRefreshBestEffort(parent context.Context) (<-chan struct{}, context.CancelFunc) {
	if c == nil || c.events == nil {
		return nil, func() {}
	}
	ctx, cancel := context.WithCancel(parent)
	events, err := c.events.Subscribe(ctx)
	if err != nil {
		cancel()
		return nil, func() {}
	}
	return events, cancel
}

func (c *notifyCommand) notifySidebarPickerOptions(store notifyStore, entries []notify.Notification, severities, sources []string, limit int, locale i18n.Locale) intpicker.Options {
	refresh := func() (intpicker.DeferredUpdate, error) {
		return c.notifySidebarDeferredUpdate(store, severities, sources, limit, locale)
	}
	ack := func(ctx intpicker.ActionContext) (intpicker.DeferredUpdate, error) {
		id := strings.TrimSpace(ctx.Value)
		if id == "" || id == notifySidebarEmptyValue {
			return refresh()
		}
		if err := store.Ack(id); err != nil {
			return intpicker.DeferredUpdate{}, fmt.Errorf("ack notification: %w", err)
		}
		return refresh()
	}
	clearNonCritical := func(ctx intpicker.ActionContext) (intpicker.DeferredUpdate, error) {
		current, err := c.notifySidebarFilteredEntries(store, severities, sources, limit)
		if err != nil {
			return intpicker.DeferredUpdate{}, err
		}
		if err := ackNonCriticalNotifications(store, current); err != nil {
			return intpicker.DeferredUpdate{}, err
		}
		return refresh()
	}
	actions := append(
		pickerCloseActionsForToggles(c.homeDir, c.lookupEnv, []string{"NotifySidebarToggle"}, "esc", "alt-2"),
		notifySidebarMutableActions(effectivePickerKeysForActions(c.homeDir, c.lookupEnv, []string{"NotifySidebar:Ack"}, []string{"a"}), ack)...,
	)
	actions = append(actions,
		notifySidebarMutableActions(effectivePickerKeysForActions(c.homeDir, c.lookupEnv, []string{"NotifySidebar:ClearNonCritical"}, []string{"x"}), clearNonCritical)...,
	)
	for _, key := range effectivePickerKeysForActions(c.homeDir, c.lookupEnv, []string{"NotifySidebar:ClearAll"}, []string{"ctrl-x"}) {
		actions = append(actions, intpicker.Action{Key: key, Intent: intpicker.ActionAccept})
	}

	now := c.clock()
	liveByID := c.notifyLiveByIDBestEffort()
	return intpicker.Options{
		UI:            "notify-sidebar",
		MultiLine:     true,
		Title:         theme.ANSINotifyTitleStart + notifyHeaderDecorator(c.statusbarDecoration()) + "Pending Notifications" + theme.ANSIReset,
		Prompt:        "Notify > ",
		Header:        "Newest first",
		Footer:        notifySidebarFooter(c.homeDir, c.lookupEnv),
		Actions:       actions,
		Items:         notifySidebarPickerItems(notifySidebarEntriesWithLiveLocale(entries, now, liveByID, locale)),
		DisableSearch: true,
	}
}

func notifySidebarMutableActions(keys []string, mutate func(intpicker.ActionContext) (intpicker.DeferredUpdate, error)) []intpicker.Action {
	actions := make([]intpicker.Action, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		actions = append(actions, intpicker.Action{Key: key, Intent: intpicker.ActionCustom, Mutate: mutate})
	}
	return actions
}

func notifySidebarFooter(homeDir func() (string, error), lookupEnv func(string) string) string {
	return pickerActionKeyGuide(homeDir, lookupEnv, []pickerActionKeyGuideItem{
		{ActionID: "NotifySidebar:FocusAndAck", Label: "focus/ack"},
		{ActionID: "NotifySidebar:Ack", Label: "ack"},
		{ActionID: "NotifySidebar:ClearNonCritical", Label: "clear non-critical"},
		{ActionID: "NotifySidebar:ClearAll", Label: "clear all"},
	})
}

func (c *notifyCommand) notifySidebarDeferredUpdate(store notifyStore, severities, sources []string, limit int, locale i18n.Locale) (intpicker.DeferredUpdate, error) {
	entries, err := c.notifySidebarFilteredEntries(store, severities, sources, limit)
	if err != nil {
		return intpicker.DeferredUpdate{}, err
	}
	now := c.clock()
	liveByID := c.notifyLiveByIDBestEffort()
	return intpicker.DeferredUpdate{
		Items: notifySidebarPickerItems(notifySidebarEntriesWithLiveLocale(entries, now, liveByID, locale)),
	}, nil
}

func (c *notifyCommand) notifySidebarFilteredEntries(store notifyStore, severities, sources []string, limit int) ([]notify.Notification, error) {
	entries, err := store.List()
	if err != nil {
		return nil, fmt.Errorf("list notifications: %w", err)
	}
	entries = filterEntries(entries, severities, sources)
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

func notifySidebarPickerItems(entries []intpickercompat.Entry) []intpicker.Item {
	items := make([]intpicker.Item, 0, len(entries))
	for _, entry := range entries {
		items = append(items, intpicker.Item{
			Label:      entry.Label,
			Title:      entry.Label,
			Value:      entry.Value,
			SearchText: entry.SearchKey,
		})
	}
	return items
}

func ackNonCriticalNotifications(store notifyStore, entries []notify.Notification) error {
	for _, entry := range entries {
		if entry.Severity == notify.SeverityCritical {
			continue
		}
		if err := store.Ack(entry.ID); err != nil {
			return fmt.Errorf("clear non-critical notification: %w", err)
		}
	}
	return nil
}

const notifySidebarEmptyValue = "__projmux_notify_empty__"

func notifySidebarEntries(entries []notify.Notification, now time.Time) []intpickercompat.Entry {
	return notifySidebarEntriesWithLive(entries, now, nil)
}

func notifySidebarEntriesWithLiveLocale(entries []notify.Notification, now time.Time, liveByID map[string]notifyLivePane, locale i18n.Locale) []intpickercompat.Entry {
	return buildNotifySidebarReadModel(entries, now, liveByID, locale).CollapsedEntries()
}

// notifySidebarEntriesWithLive renders sidebar rows with awareness of
// stale/gone display state. `liveByID` may be nil when live data is
// unavailable; in that case rows fall back to the live-row palette so a
// missing tmux server does not falsely dim every entry.
func notifySidebarEntriesWithLive(entries []notify.Notification, now time.Time, liveByID map[string]notifyLivePane) []intpickercompat.Entry {
	return notifySidebarEntriesWithLiveLocale(entries, now, liveByID, i18n.FallbackLocale)
}

type notifySidebarReadModel struct {
	Groups []notifySidebarGroup
}

type notifySidebarGroup struct {
	Key         string
	Label       string
	Project     string
	Count       int
	NewestAt    time.Time
	Worst       string
	Display     notifyRowDisplayState
	Latest      notify.Notification
	LatestLabel string
	Rows        []notifySidebarRow
}

type notifySidebarRow struct {
	GroupKey string
	Entry    intpickercompat.Entry
	Notify   notify.Notification
	Display  notifyRowDisplayState
}

func (m notifySidebarReadModel) CollapsedEntries() []intpickercompat.Entry {
	if len(m.Groups) == 0 {
		return notifySidebarEmptyEntries()
	}
	out := make([]intpickercompat.Entry, 0, len(m.Groups))
	for _, group := range m.Groups {
		out = append(out, intpickercompat.Entry{
			Label: group.Label,
			Value: group.Latest.ID,
		})
	}
	return out
}

func (m notifySidebarReadModel) ExpandedEntries(expanded map[string]bool) []intpickercompat.Entry {
	if len(m.Groups) == 0 {
		return notifySidebarEmptyEntries()
	}
	out := make([]intpickercompat.Entry, 0, len(m.Groups))
	for _, group := range m.Groups {
		out = append(out, intpickercompat.Entry{
			Label: group.Label,
			Value: group.Latest.ID,
		})
		if !expanded[group.Key] {
			continue
		}
		for _, row := range group.Rows {
			out = append(out, row.Entry)
		}
	}
	return out
}

func notifySidebarEmptyEntries() []intpickercompat.Entry {
	return []intpickercompat.Entry{{
		Label: "No pending notifications",
		Value: notifySidebarEmptyValue,
	}}
}

func buildNotifySidebarReadModel(entries []notify.Notification, now time.Time, liveByID map[string]notifyLivePane, locale i18n.Locale) notifySidebarReadModel {
	type groupBuild struct {
		group notifySidebarGroup
	}
	builders := make(map[string]*groupBuild)
	order := make([]string, 0, len(entries))
	for _, entry := range entries {
		key := notifySidebarGroupKey(entry)
		builder, ok := builders[key]
		if !ok {
			builder = &groupBuild{group: notifySidebarGroup{
				Key:     key,
				Project: notifyProjectName(entry.Session),
				Worst:   notify.SeverityInfo,
			}}
			builders[key] = builder
			order = append(order, key)
		}
		display := classifyNotifyRowState(entry, liveByID)
		label := notifySidebarLabelForLocale(entry, now, display, locale)
		builder.group.Rows = append(builder.group.Rows, notifySidebarRow{
			GroupKey: key,
			Entry: intpickercompat.Entry{
				Label: label,
				Value: entry.ID,
			},
			Notify:  entry,
			Display: display,
		})
		builder.group.Worst = notifySidebarWorstSeverity(builder.group.Worst, entry.Severity)
		builder.group.Display = notifySidebarWorstDisplay(builder.group.Display, display)
		if builder.group.Count == 0 || entry.CreatedAt.After(builder.group.NewestAt) {
			builder.group.NewestAt = entry.CreatedAt
			builder.group.Latest = entry
			builder.group.LatestLabel = label
		}
		builder.group.Count++
	}

	groups := make([]notifySidebarGroup, 0, len(order))
	for _, key := range order {
		group := builders[key].group
		sort.SliceStable(group.Rows, func(i, j int) bool {
			return group.Rows[i].Notify.CreatedAt.After(group.Rows[j].Notify.CreatedAt)
		})
		group.Label = notifySidebarGroupLabel(group, liveByID, now, locale)
		groups = append(groups, group)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		return groups[i].NewestAt.After(groups[j].NewestAt)
	})
	return notifySidebarReadModel{Groups: groups}
}

func notifySidebarGroupKey(entry notify.Notification) string {
	socket := strings.TrimSpace(entry.Socket)
	session := strings.TrimSpace(entry.Session)
	window := strings.TrimSpace(entry.Window)
	pane := strings.TrimSpace(entry.Pane)
	switch {
	case session != "" && pane != "":
		return "pane\x00" + socket + "\x00" + session + "\x00" + pane
	case session != "" && window != "":
		return "window\x00" + socket + "\x00" + session + "\x00" + window
	case session != "":
		return "session\x00" + socket + "\x00" + session
	default:
		return "external\x00" + socket
	}
}

func notifySidebarWorstSeverity(current, next string) string {
	if notifySidebarSeverityRank(next) > notifySidebarSeverityRank(current) {
		return next
	}
	if strings.TrimSpace(current) == "" {
		return notify.SeverityInfo
	}
	return current
}

func notifySidebarSeverityRank(severity string) int {
	switch severity {
	case notify.SeverityCritical:
		return 3
	case notify.SeverityWarn:
		return 2
	default:
		return 1
	}
}

func notifySidebarWorstDisplay(current, next notifyRowDisplayState) notifyRowDisplayState {
	if notifySidebarDisplayRank(next) > notifySidebarDisplayRank(current) {
		return next
	}
	return current
}

func notifySidebarDisplayRank(display notifyRowDisplayState) int {
	switch display {
	case notifyDisplayGone:
		return 3
	case notifyDisplayStale:
		return 2
	default:
		return 1
	}
}

func notifySidebarGroupLabel(group notifySidebarGroup, liveByID map[string]notifyLivePane, now time.Time, locale i18n.Locale) string {
	latest := group.Latest
	preview := notifySidebarGroupPreview(latest, locale)
	if preview == "" {
		preview = "(empty notification)"
	}
	if group.Display != notifyDisplayLive {
		preview = notifySidebarDimText(preview)
	}
	title := notifySidebarGroupTitle(group, liveByID, locale)
	count := i18n.FormatCount(group.Count, i18n.CountNotifications, locale, i18n.FormatCompact)
	ageDuration := time.Duration(0)
	if !group.NewestAt.IsZero() {
		ageDuration = now.Sub(group.NewestAt)
	}
	age := formatAgeLocale(ageDuration, locale)
	stateEntry := latest
	stateEntry.Severity = group.Worst
	stateBadge := notifySidebarStateBadgeForDisplay(notifyStateLabelForLocale(stateEntry, preview, group.Display, locale), group.Display)
	firstLine := "▸ " + title + "  " + notifySidebarDim(count) + " " + stateBadge + " " + notifySidebarAge(age)

	metaParts := []string{notifySidebarProjectBadge(group.Project)}
	if target := notifySidebarGroupTarget(latest, locale); target != "" {
		metaParts = append(metaParts, target)
	}
	return firstLine + "\n  " + strings.Join(metaParts, " ") + " " + preview
}

func notifySidebarGroupPreview(entry notify.Notification, locale i18n.Locale) string {
	if entry.Source == notify.SourceAI {
		text := renderAINotifyText(entry.Text, entry.Metadata, locale).Full
		if text == "" {
			text = entry.Text
		}
		if notifySidebarLabelCell(entry.Metadata["topic"]) == notifySidebarLabelCell(entry.Text) {
			text = "Ready"
		}
		return notifySidebarLabelCell(text)
	}
	_, text := splitAgentPrefix(entry)
	return notifySidebarLabelCell(text)
}

func notifySidebarGroupTitle(group notifySidebarGroup, liveByID map[string]notifyLivePane, locale i18n.Locale) string {
	latest := group.Latest
	agent := notifySidebarGroupExplicitAgent(group.Rows, liveByID)
	context := notifySidebarGroupContext(group.Rows, liveByID)
	fallback := notifySidebarGroupFallbackLabel(latest, locale)
	if agent == "" {
		agent = notifySidebarGroupSourceLabel(latest)
	}
	switch {
	case agent != "" && context != "":
		return agent + " · " + notifySidebarLabelCell(context)
	case agent != "" && fallback != "":
		return agent + " · " + fallback
	case agent != "":
		return agent
	case context != "":
		return notifySidebarLabelCell(context)
	default:
		return fallback
	}
}

func notifySidebarGroupExplicitAgent(rows []notifySidebarRow, liveByID map[string]notifyLivePane) string {
	for _, row := range rows {
		live := liveByID[row.Notify.ID]
		if agent := firstNonEmptyNotifySidebarString(live.Agent, row.Notify.Metadata["agent"], row.Notify.Metadata["provider"]); agent != "" {
			return notifySidebarAgentDisplayName(agent)
		}
	}
	return ""
}

func notifySidebarGroupContext(rows []notifySidebarRow, liveByID map[string]notifyLivePane) string {
	for _, row := range rows {
		live := liveByID[row.Notify.ID]
		if context := firstNonEmptyNotifySidebarString(
			live.Topic,
			row.Notify.Metadata["topic"],
			live.Title,
			row.Notify.Metadata["pane_title"],
			row.Notify.Metadata["title"],
		); context != "" {
			return notifySidebarLabelCell(context)
		}
	}
	return ""
}

func notifySidebarGroupSourceLabel(entry notify.Notification) string {
	switch entry.Source {
	case notify.SourceAI:
		return "AI"
	case notify.SourceK8s:
		return "K8s"
	case notify.SourceGit:
		return "Git"
	}
	return ""
}

func notifySidebarAgentDisplayName(agent string) string {
	agent = notifySidebarLabelCell(agent)
	switch strings.ToLower(agent) {
	case "codex":
		return "Codex"
	case "claude":
		return "Claude"
	case "antigravity":
		return "Antigravity"
	default:
		if agent == "" {
			return ""
		}
		lower := strings.ToLower(agent)
		return strings.ToUpper(lower[:1]) + lower[1:]
	}
}

func notifySidebarGroupFallbackLabel(entry notify.Notification, locale i18n.Locale) string {
	if pane := notifySidebarPlainTargetPart(i18n.TargetPane, entry.Pane, locale); pane != "" {
		return pane
	}
	if window := notifySidebarPlainTargetPart(i18n.TargetWindow, entry.Window, locale); window != "" {
		return window
	}
	if session := notifyProjectName(entry.Session); session != "" {
		return session
	}
	return "external"
}

func notifySidebarPlainTargetPart(kind i18n.TargetKind, value string, locale i18n.Locale) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.TrimLeft(value, "@%")
	value = notifySidebarLabelCell(value)
	if value == "" {
		return ""
	}
	number := parsePositiveInt(value)
	if number > 0 {
		return i18n.FormatTargetLabel(kind, number, locale, i18n.FormatCompact)
	}
	switch kind {
	case i18n.TargetWindow:
		return "window " + value
	case i18n.TargetPane:
		return "pane " + value
	default:
		return value
	}
}

func notifySidebarGroupTarget(entry notify.Notification, locale i18n.Locale) string {
	parts := make([]string, 0, 2)
	if window := notifySidebarTargetPart("window", entry.Window, locale); window != "" {
		parts = append(parts, window)
	}
	if pane := notifySidebarTargetPart("pane", entry.Pane, locale); pane != "" {
		parts = append(parts, pane)
	}
	return strings.Join(parts, " ")
}

func firstNonEmptyNotifySidebarString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func notifySidebarLabel(e notify.Notification, now time.Time) string {
	return notifySidebarLabelFor(e, now, notifyDisplayLive)
}

func notifySidebarLabelFor(e notify.Notification, now time.Time, display notifyRowDisplayState) string {
	return notifySidebarLabelForLocale(e, now, display, i18n.FallbackLocale)
}

func notifySidebarLabelForLocale(e notify.Notification, now time.Time, display notifyRowDisplayState, locale i18n.Locale) string {
	age := formatAgeLocale(now.Sub(e.CreatedAt), locale)
	agent, text := splitAgentPrefix(e)
	if e.Source == notify.SourceAI {
		text = renderAINotifyText(e.Text, e.Metadata, locale).Full
		if text == "" {
			text = e.Text
		}
		agent = ""
	}
	text = notifySidebarLabelCell(text)
	if text == "" {
		text = "(empty notification)"
	}
	if display != notifyDisplayLive {
		text = notifySidebarDimText(text)
	}
	if e.Source == notify.SourceAI {
		if notifySidebarLabelCell(e.Metadata["topic"]) == notifySidebarLabelCell(e.Text) {
			text = "Ready"
			if display != notifyDisplayLive {
				text = notifySidebarDimText(text)
			}
		}
		return notifySidebarAILabel(e, age, agent, text, display, locale)
	}
	metaParts := []string{
		notifySidebarAge(age),
		notifySidebarProjectBadge(notifyProjectName(e.Session)),
	}
	if agent != "" {
		metaParts = append(metaParts, notifySidebarAgentBadge(agent))
	}
	metaParts = append(metaParts, notifySidebarStateBadgeForDisplay(notifyStateLabelForLocale(e, text, display, locale), display))
	if notifySidebarShowTargetParts(e) {
		if window := notifySidebarTargetPart("window", e.Window, locale); window != "" {
			metaParts = append(metaParts, window)
		}
		if pane := notifySidebarTargetPart("pane", e.Pane, locale); pane != "" {
			metaParts = append(metaParts, pane)
		}
	}
	return text + "\n  " + strings.Join(metaParts, " ")
}

func notifySidebarAILabel(e notify.Notification, age, agent, text string, display notifyRowDisplayState, locale i18n.Locale) string {
	firstLineParts := []string{notifySidebarProjectBadge(notifyProjectName(e.Session))}
	if text != "" {
		firstLineParts = append(firstLineParts, text)
	}
	firstLine := strings.Join(firstLineParts, " ")

	metaParts := []string{notifySidebarAge(age)}
	if topic := notifySidebarTopicBadge(e); topic != "" {
		metaParts = append(metaParts, topic)
	}
	metaParts = append(metaParts, notifySidebarStateBadgeForDisplay(notifyStateLabelForLocale(e, text, display, locale), display))
	if agent != "" {
		metaParts = append(metaParts, notifySidebarAgentBadge(agent))
	}
	return firstLine + "\n  " + strings.Join(metaParts, " ")
}

func notifySidebarShowTargetParts(e notify.Notification) bool {
	return e.Source != notify.SourceAI
}

// notifySidebarDimText wraps the row's body text in the dim foreground so
// STALE/GONE rows visibly recede from active ones.
func notifySidebarDimText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return text
	}
	return notifySidebarDimOpen + text + notifySidebarReset
}

func notifySidebarLabelCell(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "\t", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return strings.TrimSpace(value)
}

func notifySidebarTarget(e notify.Notification) string {
	pane := strings.TrimSpace(e.Pane)
	if strings.HasPrefix(pane, "%") {
		pane = ""
	}
	return notifySidebarLabelCell(notify.FormatTarget(notify.Target{
		Session: e.Session,
		Window:  e.Window,
		Pane:    pane,
	}))
}

const (
	notifySidebarReset   = theme.ANSIReset
	notifySidebarDimOpen = theme.ANSINotifyDimStart
	notifySidebarProject = theme.ANSINotifyProjectStart
	notifySidebarInfo    = theme.ANSINotifyInfoStart
	notifySidebarWarn    = theme.ANSINotifyWarnStart
	notifySidebarCrit    = theme.ANSINotifyCritStart
	// Stale/gone badges share a muted grey palette so the ack-only state is
	// visually distinct from active rows without competing for attention.
	// STALE keeps the dim italic-equivalent (no italic SGR is universally
	// supported on tmux palettes, so we lean on the dim attribute), while
	// GONE adds strikethrough to telegraph "the target no longer exists".
	notifySidebarStale = theme.ANSINotifyStaleStart
	notifySidebarGone  = theme.ANSINotifyGoneStart
)

func notifySidebarAge(age string) string {
	age = strings.TrimSpace(age)
	if age == "" {
		age = "just now"
	}
	return theme.ANSINotifyAgeStart + " " + age + " " + notifySidebarReset
}

func notifySidebarProjectBadge(project string) string {
	project = strings.TrimSpace(project)
	if project == "" {
		project = "project"
	}
	return notifySidebarProject + " " + project + " " + notifySidebarReset
}

func notifySidebarTopicBadge(e notify.Notification) string {
	if e.Source != notify.SourceAI {
		return ""
	}
	topic := notifySidebarLabelCell(e.Metadata["topic"])
	if topic == "" {
		return ""
	}
	return theme.ANSIChipActiveStart + " " + topic + " " + notifySidebarReset
}

func notifySidebarTargetPart(label, value string, locale i18n.Locale) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.TrimLeft(value, "@%")
	value = notifySidebarLabelCell(value)
	if value == "" {
		return ""
	}
	number := parsePositiveInt(value)
	if number > 0 {
		switch label {
		case "window":
			return notifySidebarDim(i18n.FormatTargetLabel(i18n.TargetWindow, number, locale, i18n.FormatCompact))
		case "pane":
			return notifySidebarDim(i18n.FormatTargetLabel(i18n.TargetPane, number, locale, i18n.FormatCompact))
		}
	}
	return notifySidebarDim(label + " " + value)
}

func notifySidebarStateBadge(label string) string {
	return notifySidebarStateBadgeForDisplay(label, notifyDisplayLive)
}

func notifySidebarStateBadgeForDisplay(label string, display notifyRowDisplayState) string {
	label = strings.ToUpper(strings.TrimSpace(label))
	if label == "" {
		label = "INFO"
	}
	open := notifySidebarInfo
	switch {
	case display == notifyDisplayStale:
		open = notifySidebarStale
	case display == notifyDisplayGone:
		open = notifySidebarGone
	case label == "STALE":
		open = notifySidebarStale
	case label == "GONE":
		open = notifySidebarGone
	case label == "WARN":
		open = notifySidebarWarn
	case label == "CRIT":
		open = notifySidebarCrit
	}
	return open + " " + label + " " + notifySidebarReset
}

func notifySidebarAgentBadge(agent string) string {
	agent = strings.TrimSpace(agent)
	if agent == "" {
		return ""
	}
	return notifySidebarAgentOpen(agent) + " " + agent + " " + notifySidebarReset
}

func notifySidebarAgentOpen(agent string) string {
	switch strings.ToLower(strings.TrimSpace(agent)) {
	case "claude":
		return theme.ANSINotifyAgentStart
	case "codex":
		return theme.ANSINotifyAgentStart
	default:
		return theme.ANSINotifyAgentStart
	}
}

func notifySidebarDim(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return notifySidebarDimOpen + value + notifySidebarReset
}

func findNotificationByID(entries []notify.Notification, id string) (notify.Notification, bool) {
	for _, e := range entries {
		if e.ID == id {
			return e, true
		}
	}
	return notify.Notification{}, false
}

func (c *notifyCommand) notifyOriginClient(explicit string) string {
	if client := strings.TrimSpace(explicit); client != "" {
		return client
	}
	if c.lookupEnv == nil {
		return ""
	}
	if client := strings.TrimSpace(c.lookupEnv("PROJMUX_NOTIFY_ORIGIN_CLIENT")); client != "" {
		return client
	}
	return strings.TrimSpace(c.lookupEnv("PROJMUX_ORIGIN_CLIENT"))
}

func (c *notifyCommand) focusNotification(entry notify.Notification, source, kind, clientTTY string) error {
	if c.runner == nil {
		return errors.New("notify focus runner is not configured")
	}
	if c.executable == nil {
		return errors.New("notify executable resolver is not configured")
	}
	binaryPath, err := c.executable()
	if err != nil {
		return fmt.Errorf("resolve notify executable: %w", err)
	}
	target := notify.FormatTarget(notify.Target{
		Session: entry.Session,
		Window:  entry.Window,
		Pane:    entry.Pane,
	})
	if strings.TrimSpace(target) == "" {
		return errors.New("notification has no routable target")
	}
	args := []string{"focus", "--target", target, "--source", source, "--kind", kind}
	if socket := strings.TrimSpace(entry.Socket); socket != "" {
		args = append(args, "--socket", socket)
	}
	if client := strings.TrimSpace(clientTTY); client != "" {
		args = append(args, "--client", client)
	}
	if _, err := c.runner.Run(context.Background(), binaryPath, args...); err != nil {
		return fmt.Errorf("focus notification: %w", err)
	}
	return nil
}

func (c *notifyCommand) statusbarDecoration() config.StatusbarDecoration {
	if envValue(c.lookupEnv, "TMUX") != "" && c.runner != nil {
		out, err := c.runner.Run(context.Background(), "tmux", "show-options", "-gqv", statusbarDecorationNotifyTmuxOption)
		if err == nil && strings.TrimSpace(string(out)) != "" {
			return config.NormalizeStatusbarDecoration(string(out))
		}
		out, err = c.runner.Run(context.Background(), "tmux", "show-options", "-gqv", statusbarDecorationTmuxOption)
		if err == nil && strings.TrimSpace(string(out)) != "" {
			return config.NormalizeStatusbarDecoration(string(out))
		}
	}
	return loadStatusbarDecorationForTarget(c.homeDir, c.lookupEnv, statusbarDecorationTargetNotify, loadStatusbarDecoration(c.homeDir, c.lookupEnv))
}

// --- ack ---------------------------------------------------------------------

func (c *notifyCommand) runAck(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("notify ack", flag.ContinueOnError)
	fs.SetOutput(stderr)
	all := fs.Bool("all", false, "remove every queued entry")

	if err := fs.Parse(args); err != nil {
		return usageError(fmt.Sprintf("parse notify ack flags: %v", err))
	}

	store, err := c.requireStore()
	if err != nil {
		return err
	}

	if *all {
		if fs.NArg() != 0 {
			printNotifyUsage(stderr)
			return usageError("notify ack --all does not accept positional arguments")
		}
		removed, err := store.AckAll()
		if err != nil {
			return fmt.Errorf("ack all notifications: %w", err)
		}
		_, err = fmt.Fprintf(stdout, "ack %d notification(s)\n", removed)
		return err
	}

	if fs.NArg() != 1 {
		printNotifyUsage(stderr)
		return usageError("notify ack requires exactly 1 <id> argument or --all")
	}
	id := strings.TrimSpace(fs.Arg(0))
	if id == "" {
		printNotifyUsage(stderr)
		return usageError("notify ack requires a non-empty <id> argument")
	}
	if err := store.Ack(id); err != nil {
		if errors.Is(err, notify.ErrNotFound) {
			return fmt.Errorf("ack notification: %w", err)
		}
		return fmt.Errorf("ack notification: %w", err)
	}
	_, err = fmt.Fprintf(stdout, "ack %s\n", id)
	return err
}

// --- helpers -----------------------------------------------------------------

func filterEntries(entries []notify.Notification, severities, sources []string) []notify.Notification {
	if len(severities) == 0 && len(sources) == 0 {
		return entries
	}
	sevSet := toSet(severities)
	srcSet := toSet(sources)
	out := make([]notify.Notification, 0, len(entries))
	for _, e := range entries {
		if len(sevSet) > 0 {
			if _, ok := sevSet[e.Severity]; !ok {
				continue
			}
		}
		if len(srcSet) > 0 {
			if _, ok := srcSet[e.Source]; !ok {
				continue
			}
		}
		out = append(out, e)
	}
	return out
}

func toSet(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(values))
	for _, v := range values {
		out[v] = struct{}{}
	}
	return out
}

// writeNotifyTable renders a tab-aligned table: ID  AGE  SEV  SRC  TARGET  TEXT.
func writeNotifyTable(w io.Writer, entries []notify.Notification, now time.Time, locale i18n.Locale) error {
	if _, err := fmt.Fprintln(w, "ID\tAGE\tSEV\tSRC\tTARGET\tTEXT"); err != nil {
		return err
	}
	for _, e := range entries {
		target := notify.FormatTarget(notify.Target{
			Socket:  e.Socket,
			Session: e.Session,
			Window:  e.Window,
			Pane:    e.Pane,
		})
		if _, err := fmt.Fprintf(
			w,
			"%s\t%s\t%s\t%s\t%s\t%s\n",
			e.ID,
			formatAgeLocale(now.Sub(e.CreatedAt), locale),
			e.Severity,
			e.Source,
			target,
			notifyQueueDisplayText(e, locale),
		); err != nil {
			return err
		}
	}
	return nil
}

func notifyQueueDisplayText(e notify.Notification, locale i18n.Locale) string {
	if e.Source != notify.SourceAI {
		return e.Text
	}
	rendered := renderAINotifyText(e.Text, e.Metadata, locale)
	if rendered.Full != "" {
		return rendered.Full
	}
	return e.Text
}

type notifyLiveReport struct {
	Queue  []notify.Notification `json:"queue"`
	Live   []notifyLivePane      `json:"live"`
	Rows   []notifyLiveRow       `json:"rows"`
	Errors []string              `json:"errors"`
}

type notifyLivePane struct {
	ID             string `json:"id"`
	Session        string `json:"session"`
	Window         string `json:"window,omitempty"`
	Pane           string `json:"pane"`
	Socket         string `json:"socket,omitempty"`
	Title          string `json:"title,omitempty"`
	AttentionState string `json:"attention_state"`
	AIState        string `json:"ai_state,omitempty"`
	Agent          string `json:"agent,omitempty"`
	Topic          string `json:"topic,omitempty"`
	Target         string `json:"target"`
	ShouldQueue    bool   `json:"should_queue"`
}

type notifyLiveRow struct {
	State       string               `json:"state"`
	ID          string               `json:"id,omitempty"`
	Target      string               `json:"target"`
	Text        string               `json:"text,omitempty"`
	Explanation string               `json:"explanation"`
	Queue       *notify.Notification `json:"queue,omitempty"`
	Live        *notifyLivePane      `json:"live,omitempty"`
}

func (c *notifyCommand) buildNotifyLiveReport(entries []notify.Notification) (notifyLiveReport, error) {
	return c.buildNotifyLiveReportLocale(entries, i18n.FallbackLocale)
}

func (c *notifyCommand) buildNotifyLiveReportLocale(entries []notify.Notification, locale i18n.Locale) (notifyLiveReport, error) {
	report := notifyLiveReport{
		Queue:  nonNilNotifications(entries),
		Live:   []notifyLivePane{},
		Rows:   []notifyLiveRow{},
		Errors: []string{},
	}

	panes, err := c.listNotifyLivePanes()
	if err != nil {
		report.Errors = append(report.Errors, err.Error())
		for _, entry := range entries {
			report.Rows = append(report.Rows, notifyLiveQueueOnlyRow(entry, "live-unavailable", notifyLiveExplanation("live-unavailable", locale)))
		}
		return report, nil
	}

	liveByID := make(map[string]notifyLivePane, len(panes))
	for _, live := range panes {
		report.Live = append(report.Live, live)
		if live.ShouldQueue {
			liveByID[live.ID] = live
		}
	}

	queueByID := make(map[string]notify.Notification, len(entries))
	for _, entry := range entries {
		queueByID[entry.ID] = entry
	}

	for _, live := range report.Live {
		liveCopy := live
		if !live.ShouldQueue {
			state := "live-title-attention"
			if live.AttentionState == attentionStateReply {
				state = "live-manual-reply"
			}
			report.Rows = append(report.Rows, notifyLiveRow{
				State:       state,
				ID:          live.ID,
				Target:      live.Target,
				Explanation: notifyLiveExplanation(state, locale),
				Live:        &liveCopy,
			})
			continue
		}
		if entry, ok := queueByID[live.ID]; ok {
			entryCopy := entry
			report.Rows = append(report.Rows, notifyLiveRow{
				State:       "live-ai-reply-queued",
				ID:          live.ID,
				Target:      live.Target,
				Text:        notifyQueueDisplayText(entry, locale),
				Explanation: notifyLiveExplanation("live-ai-reply-queued", locale),
				Queue:       &entryCopy,
				Live:        &liveCopy,
			})
			continue
		}
		report.Rows = append(report.Rows, notifyLiveRow{
			State:       "live-ai-reply-missing-queue",
			ID:          live.ID,
			Target:      live.Target,
			Explanation: notifyLiveExplanation("live-ai-reply-missing-queue", locale),
			Live:        &liveCopy,
		})
	}

	for _, entry := range entries {
		if _, ok := liveByID[entry.ID]; ok {
			continue
		}
		state := "queue-only"
		switch classifyNotifyRowState(entry, liveByID) {
		case notifyDisplayStale:
			state = "queue-stale"
		case notifyDisplayGone:
			state = "queue-gone"
		}
		report.Rows = append(report.Rows, notifyLiveQueueOnlyRow(entry, state, notifyLiveExplanation(state, locale)))
	}

	return report, nil
}

func notifyLiveExplanation(state string, locale i18n.Locale) string {
	key, fallback := notifyLiveExplanationKey(state)
	return localizeText(locale, key, fallback)
}

func notifyLiveExplanationKey(state string) (i18n.Key, string) {
	switch strings.TrimSpace(state) {
	case "live-unavailable":
		return i18n.KeyNotifyLiveUnavailable, "live tmux pane state could not be read; queue entry remains pending"
	case "live-title-attention":
		return i18n.KeyNotifyLiveTitleAttention, "live title attention badge exists but pane is not reply+agent state; title-only/manual attention does not create notify queue entries"
	case "live-manual-reply":
		return i18n.KeyNotifyLiveManualReply, "live reply badge exists but no AI agent metadata is set; manual attention panes do not create notify queue entries"
	case "live-ai-reply-queued":
		return i18n.KeyNotifyLiveAIReplyQueued, "live AI reply pane has a matching actionable notify queue entry"
	case "live-ai-reply-missing-queue":
		return i18n.KeyNotifyLiveAIReplyMissingQueue, "live AI reply pane has no matching queue entry; run `projmux notify reconcile` to back-fill it"
	case "queue-stale":
		return i18n.KeyNotifyLiveQueueStale, "queue entry exists but live pane no longer matches reply+agent state; it remains pending until explicit ack"
	case "queue-gone":
		return i18n.KeyNotifyLiveQueueGone, "queue entry has no routable target; it can only be ack'd"
	default:
		return i18n.KeyNotifyLiveQueuePending, "queue entry is pending; no matching live AI reply pane was found"
	}
}

// notifyLiveByIDBestEffort returns the map of live AI reply-state panes keyed
// by notify id, swallowing any tmux error. The sidebar/statusbar use this so
// a missing tmux server only suppresses the stale/gone classification —
// listing entries themselves continues to work.
func (c *notifyCommand) notifyLiveByIDBestEffort() map[string]notifyLivePane {
	if c == nil || c.runner == nil {
		return nil
	}
	panes, err := c.listNotifyLivePanes()
	if err != nil {
		return nil
	}
	return notifyLiveShouldQueueByID(panes)
}

// notifyLiveShouldQueueByID indexes the subset of live panes that satisfy
// AI reply+agent state (i.e. ShouldQueue). This is the same condition
// [notifyCommand.buildNotifyLiveReport] uses to decide which queue entries
// are stale.
func notifyLiveShouldQueueByID(panes []notifyLivePane) map[string]notifyLivePane {
	out := make(map[string]notifyLivePane, len(panes))
	for _, live := range panes {
		if live.ShouldQueue {
			out[live.ID] = live
		}
	}
	return out
}

func (c *notifyCommand) listNotifyLivePanes() ([]notifyLivePane, error) {
	rows, err := (&attentionCommand{runner: c.runner}).listAttentionPanes()
	if err != nil {
		return nil, err
	}

	out := make([]notifyLivePane, 0, len(rows))
	for _, row := range rows {
		live := notifyLivePane{
			ID:             buildAttentionNotifyID(row.Session, row.Pane),
			Session:        row.Session,
			Window:         row.Window,
			Pane:           row.Pane,
			Socket:         row.Socket,
			Title:          row.Title,
			AttentionState: row.AttentionState,
			AIState:        row.AIState,
			Agent:          row.Agent,
			Topic:          row.Topic,
			Target: notify.FormatTarget(notify.Target{
				Session: row.Session,
				Window:  row.Window,
				Pane:    row.Pane,
			}),
			ShouldQueue: row.AttentionState == attentionStateReply && strings.TrimSpace(row.Agent) != "",
		}
		if live.AttentionState == attentionStateReply || hasAttentionPrefix(live.Title) || hasBraillePrefix(live.Title) {
			out = append(out, live)
		}
	}
	return out, nil
}

func nonNilNotifications(entries []notify.Notification) []notify.Notification {
	if entries == nil {
		return []notify.Notification{}
	}
	return entries
}

func notifyLiveQueueOnlyRow(entry notify.Notification, state, explanation string) notifyLiveRow {
	entryCopy := entry
	return notifyLiveRow{
		State: state,
		ID:    entry.ID,
		Target: notify.FormatTarget(notify.Target{
			Socket:  entry.Socket,
			Session: entry.Session,
			Window:  entry.Window,
			Pane:    entry.Pane,
		}),
		Text:        entry.Text,
		Explanation: explanation,
		Queue:       &entryCopy,
	}
}

func writeNotifyLiveTable(w io.Writer, report notifyLiveReport, now time.Time, locale i18n.Locale) error {
	if err := writeNotifyTable(w, report.Queue, now, locale); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, ""); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "STATE\tTARGET\tID\tEXPLANATION\tTEXT"); err != nil {
		return err
	}
	for _, row := range report.Rows {
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			notifyTableCell(row.State),
			notifyTableCell(row.Target),
			notifyTableCell(row.ID),
			notifyTableCell(row.Explanation),
			notifyTableCell(row.Text),
		); err != nil {
			return err
		}
	}
	for _, e := range report.Errors {
		if _, err := fmt.Fprintf(w, "live error\t-\t-\t%s\t-\n", notifyTableCell(e)); err != nil {
			return err
		}
	}
	return nil
}

func notifyTableCell(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	value = strings.ReplaceAll(value, "\t", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}

// formatAge renders a duration as a compact English relative age string.
func formatAge(d time.Duration) string {
	return formatAgeLocale(d, i18n.FallbackLocale)
}

func formatAgeLocale(d time.Duration, locale i18n.Locale) string {
	if d < 0 {
		d = 0
	}
	return i18n.FormatRelativeAge(d, locale, i18n.FormatCompact)
}

// writeJSON encodes payload as a single newline-terminated json document.
func writeJSON(w io.Writer, payload any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

// isInvalidInputErr reports whether the supplied store error stems from input
// validation rather than IO. Validation errors should map to exit code 2.
func isInvalidInputErr(err error) bool {
	switch {
	case errors.Is(err, notify.ErrInvalidSeverity),
		errors.Is(err, notify.ErrInvalidSource),
		errors.Is(err, notify.ErrInvalidTarget),
		errors.Is(err, notify.ErrInvalidText),
		errors.Is(err, notify.ErrInvalidTTL):
		return true
	}
	return false
}

// multiFlag collects repeated string flag values (e.g. --severity warn
// --severity critical).
type multiFlag []string

func (m *multiFlag) String() string {
	if m == nil {
		return ""
	}
	return strings.Join(*m, ",")
}

func (m *multiFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	*m = append(*m, value)
	return nil
}

func printNotifyUsage(w io.Writer) {
	fmt.Fprintln(w, "Pending AI notify queue. Attention is live pane state; notify rows remain until explicit ack.")
	fmt.Fprintln(w, "Use `notify list --live` to explain queue/live drift and `notify reconcile` to repair AI reply entries.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux notify push  --text <s> --target <SESSION[:WINDOW[.PANE]]> [--socket <s>]")
	fmt.Fprintln(w, "                        [--severity info|warn|critical] [--source ai|k8s|git|external]")
	fmt.Fprintln(w, "                        [--ttl <seconds>] [--id <s>] [--json]")
	fmt.Fprintln(w, "  projmux notify list  [--live] [--json] [--limit N] [--ui table|sidebar] [--client <tty>] [--severity ...] [--source ...]")
	fmt.Fprintln(w, "  projmux notify ack   <id> | --all")
	fmt.Fprintln(w, "  projmux notify reconcile [--json]")
}

func printNotifyListUsage(w io.Writer) {
	fmt.Fprintln(w, "Pending AI notify queue entries only; rows remain until explicit ack.")
	fmt.Fprintln(w, "Use `--live` to explain queue entries against live pane attention state without mutating either surface.")
	fmt.Fprintln(w, "Use `--ui=sidebar` for the interactive right-side notify list.")
	fmt.Fprintln(w, "Use `projmux attention list` for live pane attention state only.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux notify list [--live] [--json] [--limit N] [--ui table|sidebar] [--client <tty>] [--severity ...] [--source ...]")
}

func printNotifyReconcileUsage(w io.Writer) {
	fmt.Fprintln(w, "Repair the pending AI notify queue from live tmux pane attention state.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux notify reconcile [--json]")
}
