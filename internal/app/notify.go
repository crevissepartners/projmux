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
	defer applyNativeUIThemeFromConfig(c.homeDir, c.lookupEnv, "")()
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
	options := c.notifySidebarPickerOptions(store, entries, severities, sources, limit, clientTTY, locale)
	if trigger, cancel := c.subscribeNotifyQueueRefreshBestEffort(context.Background()); trigger != nil {
		defer cancel()
		options.DeferredUpdateTrigger = trigger
	} else {
		options.DeferredUpdate = nil
	}
	if options.Theme == nil {
		if source, err := configRenderThemeSource(c.homeDir, c.lookupEnv, ""); err == nil {
			options = source.pickerOptions(options)
		} else {
			options = fallbackRenderThemeSource().pickerOptions(options)
		}
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
		if notifySidebarGroupKeyFromValue(id) != "" {
			return nil
		}
		if err := store.Ack(id); err != nil {
			return fmt.Errorf("ack notification: %w", err)
		}
		return nil
	case pickerKeyMatchesAction(c.homeDir, c.lookupEnv, result.Key, "NotifySidebar:AckGroup", "A"):
		groupKey := notifySidebarSelectedGroupKey(entries, id)
		if groupKey == "" {
			return nil
		}
		if err := ackNotifySidebarGroup(store, entries, groupKey); err != nil {
			return err
		}
		return nil
	case pickerKeyMatchesAction(c.homeDir, c.lookupEnv, result.Key, "NotifySidebar:ClearNonCritical", "x"):
		if err := ackNonCriticalNotifications(store, entries); err != nil {
			return err
		}
		return nil
	case pickerKeyMatchesAction(c.homeDir, c.lookupEnv, result.Key, "NotifySidebar:ClearGone", "g"):
		removed, err := c.clearGoneNotifications(store, entries)
		if err != nil {
			return err
		}
		if removed == 0 {
			c.displayNotifySidebarMessage("no gone notifications")
		}
		return nil
	default:
		if groupKey := notifySidebarGroupKeyFromValue(id); groupKey != "" {
			return c.focusAndAckNotifySidebarGroup(store, entries, groupKey, clientTTY, locale)
		}
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

func (c *notifyCommand) notifySidebarPickerOptions(store notifyStore, entries []notify.Notification, severities, sources []string, limit int, clientTTY string, locale i18n.Locale) intpicker.Options {
	expanded := map[string]bool{}
	refresh := func() (intpicker.DeferredUpdate, error) {
		return c.notifySidebarDeferredUpdate(store, severities, sources, limit, locale, expanded)
	}
	ack := func(ctx intpicker.ActionContext) (intpicker.DeferredUpdate, error) {
		id := strings.TrimSpace(ctx.Value)
		if id == "" || id == notifySidebarEmptyValue {
			return refresh()
		}
		if notifySidebarGroupKeyFromValue(id) != "" {
			return refresh()
		}
		if err := store.Ack(id); err != nil {
			return intpicker.DeferredUpdate{}, fmt.Errorf("ack notification: %w", err)
		}
		return refresh()
	}
	ackGroup := func(ctx intpicker.ActionContext) (intpicker.DeferredUpdate, error) {
		current, err := c.notifySidebarFilteredEntries(store, severities, sources, limit)
		if err != nil {
			return intpicker.DeferredUpdate{}, err
		}
		groupKey := notifySidebarSelectedGroupKey(current, ctx.Value)
		if groupKey == "" {
			return refresh()
		}
		if err := ackNotifySidebarGroup(store, current, groupKey); err != nil {
			return intpicker.DeferredUpdate{}, err
		}
		delete(expanded, groupKey)
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
	clearGone := func(ctx intpicker.ActionContext) (intpicker.DeferredUpdate, error) {
		current, err := c.notifySidebarFilteredEntries(store, severities, sources, limit)
		if err != nil {
			return intpicker.DeferredUpdate{}, err
		}
		removed, err := c.clearGoneNotifications(store, current)
		if err != nil {
			return intpicker.DeferredUpdate{}, err
		}
		if removed == 0 {
			c.displayNotifySidebarMessage("no gone notifications")
		}
		return refresh()
	}
	unfold := func(ctx intpicker.ActionContext) (intpicker.DeferredUpdate, error) {
		current, err := c.notifySidebarFilteredEntries(store, severities, sources, limit)
		if err != nil {
			return intpicker.DeferredUpdate{}, err
		}
		if groupKey := notifySidebarSelectedGroupKey(current, ctx.Value); groupKey != "" {
			if notifySidebarGroupCanUnfold(current, groupKey) {
				expanded[groupKey] = true
			} else {
				delete(expanded, groupKey)
			}
		}
		return refresh()
	}
	fold := func(ctx intpicker.ActionContext) (intpicker.DeferredUpdate, error) {
		current, err := c.notifySidebarFilteredEntries(store, severities, sources, limit)
		if err != nil {
			return intpicker.DeferredUpdate{}, err
		}
		if groupKey := notifySidebarSelectedGroupKey(current, ctx.Value); groupKey != "" {
			delete(expanded, groupKey)
		}
		return refresh()
	}
	focusOrAckGroup := func(ctx intpicker.ActionContext) (intpicker.DeferredUpdate, error) {
		current, err := c.notifySidebarFilteredEntries(store, severities, sources, limit)
		if err != nil {
			return intpicker.DeferredUpdate{}, err
		}
		groupKey := notifySidebarGroupKeyFromValue(ctx.Value)
		if groupKey != "" {
			if err := c.focusAndAckNotifySidebarGroup(store, current, groupKey, clientTTY, locale); err != nil {
				return intpicker.DeferredUpdate{}, err
			}
			delete(expanded, groupKey)
			return refresh()
		}
		return intpicker.DeferredUpdate{Result: &intpicker.Result{Key: ctx.Key, Value: ctx.Value, Query: ctx.Query}}, nil
	}
	actions := append(
		pickerCloseActionsForPopupToggleMode(c.homeDir, c.lookupEnv, "notify-sidebar", "esc"),
		notifySidebarMutableActions(effectivePickerKeysForActions(c.homeDir, c.lookupEnv, []string{"NotifySidebar:FocusAndAck"}, []string{"enter"}), focusOrAckGroup)...,
	)
	actions = append(actions,
		notifySidebarMutableActions(effectivePickerKeysForActions(c.homeDir, c.lookupEnv, []string{"NotifySidebar:Ack"}, []string{"a"}), ack)...,
	)
	actions = append(actions,
		notifySidebarMutableActions(effectivePickerKeysForActions(c.homeDir, c.lookupEnv, []string{"NotifySidebar:AckGroup"}, []string{"A"}), ackGroup)...,
	)
	actions = append(actions,
		notifySidebarMutableActions(effectivePickerKeysForActions(c.homeDir, c.lookupEnv, []string{"NotifySidebar:ClearNonCritical"}, []string{"x"}), clearNonCritical)...,
	)
	actions = append(actions,
		notifySidebarMutableActions(effectivePickerKeysForActions(c.homeDir, c.lookupEnv, []string{"NotifySidebar:ClearGone"}, []string{"g"}), clearGone)...,
	)
	actions = append(actions, notifySidebarMutableActions([]string{"right"}, unfold)...)
	actions = append(actions, notifySidebarMutableActions([]string{"left"}, fold)...)
	for _, key := range effectivePickerKeysForActions(c.homeDir, c.lookupEnv, []string{"NotifySidebar:ClearAll"}, []string{"ctrl-x"}) {
		actions = append(actions, intpicker.Action{Key: key, Intent: intpicker.ActionAccept})
	}

	now := c.clock()
	liveByID, paneSet := c.notifyLiveStateBestEffort()
	model := buildNotifySidebarReadModel(entries, now, liveByID, paneSet, locale)
	return intpicker.Options{
		UI:             "notify-sidebar",
		MultiLine:      true,
		Title:          notifySidebarTitle + notifyHeaderDecorator(c.statusbarDecoration()) + localizeUIText(locale, "Pending Notifications") + theme.ANSIReset,
		Prompt:         localizeUIText(locale, "Notify > "),
		Header:         localizeUIText(locale, "Newest first"),
		Footer:         notifySidebarFooter(c.homeDir, c.lookupEnv, locale),
		Actions:        actions,
		Items:          notifySidebarPickerItems(model.ExpandedEntries(expanded)),
		DisableSearch:  true,
		DeferredUpdate: refresh,
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

func notifySidebarFooter(homeDir func() (string, error), lookupEnv func(string) string, locale i18n.Locale) string {
	guide := pickerActionKeyGuide(homeDir, lookupEnv, []pickerActionKeyGuideItem{
		{ActionID: "NotifySidebar:FocusAndAck", Label: "focus live/inactive / clean gone"},
		{ActionID: "NotifySidebar:Ack", Label: "ack child"},
		{ActionID: "NotifySidebar:AckGroup", Label: "ack group"},
		{ActionID: "NotifySidebar:ClearNonCritical", Label: "clear non-critical"},
		{ActionID: "NotifySidebar:ClearGone", Label: "clear gone"},
		{ActionID: "NotifySidebar:ClearAll", Label: "clear all"},
	})
	local := keybindingReadableChord("Right") + ": " + localizeUIText(locale, "show child rows") + "  |  " + keybindingReadableChord("Left") + ": " + localizeUIText(locale, "hide child rows")
	if guide == "" {
		return local
	}
	return local + "  |  " + guide
}

func (c *notifyCommand) notifySidebarDeferredUpdate(store notifyStore, severities, sources []string, limit int, locale i18n.Locale, expanded map[string]bool) (intpicker.DeferredUpdate, error) {
	entries, err := c.notifySidebarFilteredEntries(store, severities, sources, limit)
	if err != nil {
		return intpicker.DeferredUpdate{}, err
	}
	now := c.clock()
	liveByID, paneSet := c.notifyLiveStateBestEffort()
	model := buildNotifySidebarReadModel(entries, now, liveByID, paneSet, locale)
	pruneNotifySidebarExpanded(expanded, model)
	return intpicker.DeferredUpdate{
		Items: notifySidebarPickerItems(model.ExpandedEntries(expanded)),
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

// ackGoneNotifications dismisses every entry whose display classification is
// notifyDisplayGone, leaving live/inactive/critical rows untouched. It returns
// the number of entries acked so callers can no-op with a hint when the queue
// has nothing gone. GONE classification is reused from classifyNotifyRowState;
// this helper never changes that policy.
func ackGoneNotifications(store notifyStore, entries []notify.Notification, liveByID map[string]notifyLivePane, paneSet notifyLivePaneSet) (int, error) {
	removed := 0
	for _, entry := range entries {
		if classifyNotifyRowState(entry, liveByID, paneSet) != notifyDisplayGone {
			continue
		}
		if err := store.Ack(entry.ID); err != nil {
			return removed, fmt.Errorf("clear gone notification: %w", err)
		}
		removed++
	}
	return removed, nil
}

// clearGoneNotifications resolves best-effort live state and dismisses gone
// entries. It shares the runSidebar dispatch path and the in-picker clearGone
// handler so both surfaces classify identically.
func (c *notifyCommand) clearGoneNotifications(store notifyStore, entries []notify.Notification) (int, error) {
	liveByID, paneSet := c.notifyLiveStateBestEffort()
	return ackGoneNotifications(store, entries, liveByID, paneSet)
}

const notifySidebarEmptyValue = "__projmux_notify_empty__"
const notifySidebarGroupValuePrefix = "__projmux_notify_group__:"

func notifySidebarGroupValue(groupKey string) string {
	groupKey = strings.TrimSpace(groupKey)
	if groupKey == "" {
		return ""
	}
	return notifySidebarGroupValuePrefix + groupKey
}

func notifySidebarGroupKeyFromValue(value string) string {
	value = strings.TrimSpace(value)
	key, ok := strings.CutPrefix(value, notifySidebarGroupValuePrefix)
	if !ok {
		return ""
	}
	return key
}

func notifySidebarSelectedGroupKey(entries []notify.Notification, selectedValue string) string {
	selectedValue = strings.TrimSpace(selectedValue)
	if selectedValue == "" || selectedValue == notifySidebarEmptyValue {
		return ""
	}
	if groupKey := notifySidebarGroupKeyFromValue(selectedValue); groupKey != "" {
		return groupKey
	}
	for _, entry := range entries {
		if entry.ID == selectedValue {
			return notifySidebarGroupKey(entry)
		}
	}
	return ""
}

func ackNotifySidebarGroup(store notifyStore, entries []notify.Notification, groupKey string) error {
	groupKey = strings.TrimSpace(groupKey)
	if groupKey == "" {
		return nil
	}
	for _, entry := range entries {
		if notifySidebarGroupKey(entry) != groupKey {
			continue
		}
		if err := store.Ack(entry.ID); err != nil {
			return fmt.Errorf("ack notification group: %w", err)
		}
	}
	return nil
}

func (c *notifyCommand) focusAndAckNotifySidebarGroup(store notifyStore, entries []notify.Notification, groupKey, clientTTY string, locale i18n.Locale) error {
	liveByID, paneSet := c.notifyLiveStateBestEffort()
	group, ok := notifySidebarGroupByKey(entries, c.clock(), liveByID, paneSet, groupKey, locale)
	if !ok {
		c.displayNotifySidebarMessage("notify group already gone")
		return nil
	}
	representative, display, ok := notifySidebarGroupRepresentative(group)
	if !ok {
		c.displayNotifySidebarMessage("notify group already gone")
		return nil
	}
	if display == notifyDisplayGone {
		if err := ackNotifySidebarGroup(store, entries, groupKey); err != nil {
			return err
		}
		c.displayNotifySidebarMessage(notifySidebarGroupCleanupMessage(display))
		return nil
	}
	if err := c.focusNotification(representative, "notify-sidebar", "group-select", clientTTY); err != nil {
		if isFocusTargetUnresolved(err) {
			if ackErr := ackNotifySidebarGroup(store, entries, groupKey); ackErr != nil {
				return ackErr
			}
			c.displayNotifySidebarMessage(notifySidebarGroupCleanupMessage(notifyDisplayGone))
			return nil
		}
		c.displayNotifySidebarMessage(notifySidebarGroupFocusBlockedMessage(notifyDisplayLive, err))
		return nil
	}
	return ackNotifySidebarGroup(store, entries, groupKey)
}

func notifySidebarGroupByKey(entries []notify.Notification, now time.Time, liveByID map[string]notifyLivePane, paneSet notifyLivePaneSet, groupKey string, locale i18n.Locale) (notifySidebarGroup, bool) {
	groupKey = strings.TrimSpace(groupKey)
	if groupKey == "" {
		return notifySidebarGroup{}, false
	}
	model := buildNotifySidebarReadModel(entries, now, liveByID, paneSet, locale)
	for _, group := range model.Groups {
		if group.Key == groupKey {
			return group, true
		}
	}
	return notifySidebarGroup{}, false
}

func notifySidebarGroupRepresentative(group notifySidebarGroup) (notify.Notification, notifyRowDisplayState, bool) {
	if len(group.Rows) == 0 {
		return notify.Notification{}, notifyDisplayGone, false
	}
	for _, row := range group.Rows {
		if row.Display == notifyDisplayLive {
			return row.Notify, row.Display, true
		}
	}
	row := group.Rows[0]
	return row.Notify, row.Display, true
}

func notifySidebarGroupFocusBlockedMessage(display notifyRowDisplayState, err error) string {
	switch display {
	case notifyDisplayGone:
		return notifySidebarGroupCleanupMessage(display)
	}
	if isFocusTargetUnresolved(err) {
		return notifySidebarGroupCleanupMessage(notifyDisplayGone)
	}
	return fmt.Sprintf("notify group focus failed: %s; not acked", focusFailureSummary(err))
}

func notifySidebarGroupCleanupMessage(display notifyRowDisplayState) string {
	switch display {
	case notifyDisplayGone:
		return "notify gone group cleaned"
	default:
		return "notify group cleaned"
	}
}

func (c *notifyCommand) displayNotifySidebarMessage(message string) {
	message = strings.TrimSpace(message)
	if message == "" || c == nil || c.runner == nil {
		return
	}
	_, _ = c.runner.Run(context.Background(), "tmux", "display-message", message)
}

func pruneNotifySidebarExpanded(expanded map[string]bool, model notifySidebarReadModel) {
	if len(expanded) == 0 {
		return
	}
	visible := make(map[string]bool, len(model.Groups))
	for _, group := range model.Groups {
		visible[group.Key] = group.HasChildRows()
	}
	for key := range expanded {
		if !visible[key] {
			delete(expanded, key)
		}
	}
}

func notifySidebarGroupCanUnfold(entries []notify.Notification, groupKey string) bool {
	if strings.TrimSpace(groupKey) == "" {
		return false
	}
	count := 0
	for _, entry := range entries {
		if notifySidebarGroupKey(entry) != groupKey {
			continue
		}
		count++
		if count > 1 {
			return true
		}
	}
	return false
}

func notifySidebarEntries(entries []notify.Notification, now time.Time) []intpickercompat.Entry {
	return notifySidebarEntriesWithLive(entries, now, nil)
}

func notifySidebarEntriesWithLiveLocale(entries []notify.Notification, now time.Time, liveByID map[string]notifyLivePane, locale i18n.Locale) []intpickercompat.Entry {
	// Legacy collapsed-entry helper: the modern sidebar path builds the read
	// model directly (with the pane inventory). These helpers keep nil paneSet
	// (inventory unavailable) so behavior is unchanged for their callers.
	return buildNotifySidebarReadModel(entries, now, liveByID, nil, locale).CollapsedEntries()
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
	Locale i18n.Locale
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
		return notifySidebarEmptyEntries(m.Locale)
	}
	out := make([]intpickercompat.Entry, 0, len(m.Groups))
	for _, group := range m.Groups {
		out = append(out, notifySidebarGroupEntry(group, false))
	}
	return out
}

func (m notifySidebarReadModel) ExpandedEntries(expanded map[string]bool) []intpickercompat.Entry {
	if len(m.Groups) == 0 {
		return notifySidebarEmptyEntries(m.Locale)
	}
	out := make([]intpickercompat.Entry, 0, len(m.Groups))
	for _, group := range m.Groups {
		isExpanded := expanded[group.Key] && group.HasChildRows()
		out = append(out, notifySidebarGroupEntry(group, isExpanded))
		if !isExpanded {
			continue
		}
		for _, row := range group.Rows {
			out = append(out, row.Entry)
		}
	}
	return out
}

func (group notifySidebarGroup) ChildCount() int {
	if len(group.Rows) <= 1 {
		return 0
	}
	return len(group.Rows)
}

func (group notifySidebarGroup) HasChildRows() bool {
	return group.ChildCount() > 0
}

func notifySidebarGroupEntry(group notifySidebarGroup, expanded bool) intpickercompat.Entry {
	return intpickercompat.Entry{
		Label: notifySidebarGroupLabelWithMarker(group.Label, expanded, group.HasChildRows()),
		Value: notifySidebarGroupValue(group.Key),
	}
}

func notifySidebarGroupLabelWithMarker(label string, expanded, hasChildren bool) string {
	if !hasChildren {
		return notifySidebarGroupLabelWithoutMarker(label)
	}
	marker := "▸ "
	if expanded {
		marker = "▾ "
	}
	return marker + notifySidebarGroupLabelWithoutMarker(label)
}

func notifySidebarGroupLabelWithoutMarker(label string) string {
	if strings.HasPrefix(label, "▸ ") || strings.HasPrefix(label, "▾ ") {
		return label[len("▸ "):]
	}
	return label
}

func notifySidebarEmptyEntries(locale i18n.Locale) []intpickercompat.Entry {
	return []intpickercompat.Entry{{
		Label: localizeUIText(locale, "No pending notifications"),
		Value: notifySidebarEmptyValue,
	}}
}

func buildNotifySidebarReadModel(entries []notify.Notification, now time.Time, liveByID map[string]notifyLivePane, paneSet notifyLivePaneSet, locale i18n.Locale) notifySidebarReadModel {
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
		display := classifyNotifyRowState(entry, liveByID, paneSet)
		label := notifySidebarChildLabelForLocale(entry, now, display, locale)
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
	return notifySidebarReadModel{Groups: groups, Locale: locale}
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
	project := notifySidebarGroupProjectLabel(group)
	provider := notifySidebarGroupProvider(group, liveByID)
	context := notifySidebarGroupContextLabel(group, liveByID)
	ageDuration := time.Duration(0)
	if !group.NewestAt.IsZero() {
		ageDuration = now.Sub(group.NewestAt)
	}
	age := formatAgeLocale(ageDuration, locale)
	stateEntry := latest
	stateEntry.Severity = group.Worst
	stateBadge := notifySidebarStateBadgeForDisplay(notifyStateLabelForLocale(stateEntry, preview, group.Display, locale), group.Display)

	aggregateParts := make([]string, 0, 2)
	if childCount := notifySidebarGroupChildCount(group.ChildCount()); childCount != "" {
		aggregateParts = append(aggregateParts, notifySidebarDim(childCount))
	}
	aggregateParts = append(aggregateParts, stateBadge)

	lines := []string{
		notifySidebarProjectBadge(project) + " · " + provider + "  " + notifySidebarAge(age),
		"  " + context + "  " + strings.Join(aggregateParts, " "),
		"  " + preview,
	}
	return strings.Join(lines, "\n")
}

func notifySidebarGroupChildCount(count int) string {
	if count <= 0 {
		return ""
	}
	return fmt.Sprintf("+%d", count)
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

func notifySidebarGroupProjectLabel(group notifySidebarGroup) string {
	if project := strings.TrimSpace(group.Project); project != "" {
		return project
	}
	return notifySidebarGroupFallbackLabel(group.Latest)
}

func notifySidebarGroupProvider(group notifySidebarGroup, liveByID map[string]notifyLivePane) string {
	if agent := notifySidebarGroupExplicitAgent(group.Rows, liveByID); agent != "" {
		return agent
	}
	if source := notifySidebarGroupSourceLabel(group.Latest); source != "" {
		return source
	}
	return "Notify"
}

func notifySidebarGroupContextLabel(group notifySidebarGroup, liveByID map[string]notifyLivePane) string {
	if context := notifySidebarGroupContext(group.Rows, liveByID); context != "" {
		return notifySidebarLabelCell(context)
	}
	return "notification"
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
	case notify.SourceExternal:
		return "External"
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

func notifySidebarGroupFallbackLabel(entry notify.Notification) string {
	if session := notifyProjectName(entry.Session); session != "" {
		return session
	}
	return "external"
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

func notifySidebarChildLabelForLocale(e notify.Notification, now time.Time, display notifyRowDisplayState, locale i18n.Locale) string {
	age := formatAgeLocale(now.Sub(e.CreatedAt), locale)
	preview := notifySidebarGroupPreview(e, locale)
	if preview == "" {
		preview = "(empty notification)"
	}
	if display != notifyDisplayLive {
		preview = notifySidebarDimText(preview)
	}
	stateBadge := notifySidebarStateBadgeForDisplay(notifyStateLabelForLocale(e, preview, display, locale), display)
	parts := []string{notifySidebarAge(age), preview, stateBadge}
	if target := notifySidebarChildTarget(e, locale); target != "" {
		parts = append(parts, target)
	}
	return "  " + strings.Join(parts, " ")
}

func notifySidebarChildTarget(e notify.Notification, locale i18n.Locale) string {
	if e.Source == notify.SourceAI {
		return ""
	}
	parts := make([]string, 0, 2)
	if window := notifySidebarTargetPart("window", e.Window, locale); window != "" {
		parts = append(parts, window)
	}
	if pane := notifySidebarTargetPart("pane", e.Pane, locale); pane != "" {
		parts = append(parts, pane)
	}
	return strings.Join(parts, " ")
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
// inactive/gone rows visibly recede from active ones.
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

const notifySidebarReset = theme.ANSIReset

// notify sidebar role escapes default to fallback literals; applyNativeUITheme
// repoints them for an explicit global theme (see theme_render_native.go).
// Inactive/gone badges share a muted grey palette so target-state hints are
// visually distinct from active rows without competing for attention. INACTIVE
// keeps the dim italic-equivalent (no italic SGR is universally supported on
// tmux palettes, so we lean on the dim attribute), while GONE adds
// strikethrough to telegraph "the target no longer exists".
var (
	notifySidebarDimOpen        = theme.ANSINotifyDimStart
	notifySidebarProject        = theme.ANSINotifyProjectStart
	notifySidebarInfo           = theme.ANSINotifyInfoStart
	notifySidebarWarn           = theme.ANSINotifyWarnStart
	notifySidebarCrit           = theme.ANSINotifyCritStart
	notifySidebarStale          = theme.ANSINotifyStaleStart
	notifySidebarGone           = theme.ANSINotifyGoneStart
	notifySidebarTitle          = theme.ANSINotifyTitleStart
	notifySidebarAgeOpen        = theme.ANSINotifyAgeStart
	notifySidebarTopicOpen      = theme.ANSIChipActiveStart
	notifySidebarAgentOpenStyle = theme.ANSINotifyAgentStart
)

func notifySidebarAge(age string) string {
	age = strings.TrimSpace(age)
	if age == "" {
		age = "just now"
	}
	return notifySidebarAgeOpen + " " + age + " " + notifySidebarReset
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
	return notifySidebarTopicOpen + " " + topic + " " + notifySidebarReset
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
	case label == "INACTIVE":
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
		return notifySidebarAgentOpenStyle
	case "codex":
		return notifySidebarAgentOpenStyle
	default:
		return notifySidebarAgentOpenStyle
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

	panes, paneSet, err := c.listNotifyLivePanesAndSet()
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
		switch classifyNotifyRowState(entry, liveByID, paneSet) {
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
		return i18n.KeyNotifyLiveQueueStale, "queue entry target is inactive: the live pane no longer matches reply+agent state; it may still be focusable if the target is routable"
	case "queue-gone":
		return i18n.KeyNotifyLiveQueueGone, "queue entry target is gone: no routable target exists; Enter/ack cleans it up without focusing"
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

// notifyLiveStateBestEffort reads the attention pane rows once and returns
// both the reply+agent index (for stale detection) and the full pane inventory
// (for gone detection), swallowing any tmux error into nil/nil. Sidebar
// renders use this so a single `list-panes` subprocess feeds both classifiers.
func (c *notifyCommand) notifyLiveStateBestEffort() (map[string]notifyLivePane, notifyLivePaneSet) {
	if c == nil || c.runner == nil {
		return nil, nil
	}
	panes, paneSet, err := c.listNotifyLivePanesAndSet()
	if err != nil {
		return nil, nil
	}
	return notifyLiveShouldQueueByID(panes), paneSet
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

// notifyLivePaneSet is the full live tmux pane inventory (every pane on the
// server), used to decide whether a queue row's pane target still exists.
//
// A nil notifyLivePaneSet means "inventory unavailable" (the tmux read failed
// or returned an empty/unrecognized result); callers MUST treat nil as
// best-effort and skip membership-based GONE classification rather than
// goneing every row. An empty (non-nil) set is also treated as unavailable by
// the constructor below: it returns nil when no live panes were parsed, which
// is the same docker-e2e degradation [statusbarCommand.classifyHeadDisplayBestEffort]
// guards against.
type notifyLivePaneSet map[string]struct{}

// notifyLivePaneSetKey builds the membership key for a pane target. The notify
// queue is pane-centric, so we key by pane id scoped within its session: two
// different sessions can each have a `%0`. Pane ids are unique per tmux server,
// so session+pane is sufficient.
//
// Socket is deliberately NOT part of the key. The notify producer records a
// socket path, but user-edited and reconciled rows frequently omit it while the
// tmux-reported pane always carries one; including socket in the key would
// cause false-gone on socket-empty rows. The (session, pane) pair is precise
// enough in practice and errs toward "present" (no false gone) when sockets
// disagree.
func notifyLivePaneSetKey(session, pane string) string {
	return strings.TrimSpace(session) + "\x00" + strings.TrimSpace(pane)
}

// newNotifyLivePaneSet builds a pane-inventory set from the full (unfiltered)
// attention pane rows. It returns nil when no rows have a usable pane target,
// so callers uniformly read nil as "inventory unavailable".
func newNotifyLivePaneSet(rows []attentionPaneRow) notifyLivePaneSet {
	set := make(notifyLivePaneSet, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.Pane) == "" {
			continue
		}
		set[notifyLivePaneSetKey(row.Session, row.Pane)] = struct{}{}
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

// Has reports whether the entry's pane target is present in the live
// inventory. It is only meaningful for pane-target rows; window/session-only
// rows should not be tested for membership (see classifyNotifyRowState).
func (s notifyLivePaneSet) Has(entry notify.Notification) bool {
	if s == nil {
		return false
	}
	_, ok := s[notifyLivePaneSetKey(entry.Session, entry.Pane)]
	return ok
}

// listNotifyLivePanesAndSet reads the attention pane rows once and returns both
// the attention/reply-filtered notifyLivePane slice (for stale detection) and
// the full pane-inventory set (for GONE detection), avoiding a second tmux
// subprocess.
func (c *notifyCommand) listNotifyLivePanesAndSet() ([]notifyLivePane, notifyLivePaneSet, error) {
	rows, err := (&attentionCommand{runner: c.runner}).listAttentionPanes()
	if err != nil {
		return nil, nil, err
	}
	return notifyLivePanesFromRows(rows), newNotifyLivePaneSet(rows), nil
}

func (c *notifyCommand) listNotifyLivePanes() ([]notifyLivePane, error) {
	rows, err := (&attentionCommand{runner: c.runner}).listAttentionPanes()
	if err != nil {
		return nil, err
	}
	return notifyLivePanesFromRows(rows), nil
}

func notifyLivePanesFromRows(rows []attentionPaneRow) []notifyLivePane {
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
	return out
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
