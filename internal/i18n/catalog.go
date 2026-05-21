package i18n

import (
	"fmt"
	"maps"
)

// Key identifies one user-facing message in the catalog.
type Key string

const (
	KeyNotifyAIResponseComplete      Key = "notify.ai.response_complete"
	KeyNotifyAIApprovalRequired      Key = "notify.ai.approval_required"
	KeyNotifyAIInputRequired         Key = "notify.ai.input_required"
	KeyNotifyAISelectionRequired     Key = "notify.ai.selection_required"
	KeyNotifyAIConfirmationRequired  Key = "notify.ai.confirmation_required"
	KeyNotifyAIError                 Key = "notify.ai.error"
	KeyNotifyAISubagentStopped       Key = "notify.ai.subagent_stopped"
	KeyNotifyAITeammateWaiting       Key = "notify.ai.teammate_waiting"
	KeyNotifyAIReviewPending         Key = "notify.ai.review_pending"
	KeyNotifyLiveUnavailable         Key = "notify.live.explanation.live_unavailable"
	KeyNotifyLiveTitleAttention      Key = "notify.live.explanation.title_attention"
	KeyNotifyLiveManualReply         Key = "notify.live.explanation.manual_reply"
	KeyNotifyLiveAIReplyQueued       Key = "notify.live.explanation.ai_reply_queued"
	KeyNotifyLiveAIReplyMissingQueue Key = "notify.live.explanation.ai_reply_missing_queue"
	KeyNotifyLiveQueuePending        Key = "notify.live.explanation.queue_pending"
	KeyNotifyLiveQueueStale          Key = "notify.live.explanation.queue_stale"
	KeyNotifyLiveQueueGone           Key = "notify.live.explanation.queue_gone"
	KeyNotifyQueueRowAgeCompact      Key = "notify.queue.row.age_compact"
	KeyNotifyQueueActionAck          Key = "notify.queue.action.ack"
	KeyStatusNotifyCount             Key = "status.notify.count"
	KeyStatusNotifyAgeCompact        Key = "status.notify.age_compact"
	KeySettingsRootNotifications     Key = "settings.root.notifications"
	KeySettingsRootProject           Key = "settings.root.project"
	KeySettingsRootAppearance        Key = "settings.root.appearance"
	KeySettingsNotificationsDesktop  Key = "settings.notifications.desktop"
	KeySettingsNotificationsDelivery Key = "settings.notifications.delivery_sources"
	KeySettingsAboutWelcome          Key = "settings.about.welcome"
	KeyPickerPromptSearch            Key = "picker.prompt.search"
	KeyPickerFooterOpenRows          Key = "picker.footer.open_rows"
	KeyPickerFooterBack              Key = "picker.footer.back"
	KeyPickerFooterClose             Key = "picker.footer.close"
	KeyPickerEmptyNoMatches          Key = "picker.empty.no_matches"
	KeyWelcomeShellTitle             Key = "welcome.shell.title"
	KeyUpdateStatusAvailable         Key = "update.status.available"
	KeyHelpUsageCommand              Key = "help.usage.command"
)

var foundationKeys = []Key{
	KeyNotifyAIResponseComplete,
	KeyNotifyAIApprovalRequired,
	KeyNotifyAIInputRequired,
	KeyNotifyAISelectionRequired,
	KeyNotifyAIConfirmationRequired,
	KeyNotifyAIError,
	KeyNotifyAISubagentStopped,
	KeyNotifyAITeammateWaiting,
	KeyNotifyAIReviewPending,
	KeyNotifyLiveUnavailable,
	KeyNotifyLiveTitleAttention,
	KeyNotifyLiveManualReply,
	KeyNotifyLiveAIReplyQueued,
	KeyNotifyLiveAIReplyMissingQueue,
	KeyNotifyLiveQueuePending,
	KeyNotifyLiveQueueStale,
	KeyNotifyLiveQueueGone,
	KeyNotifyQueueRowAgeCompact,
	KeyNotifyQueueActionAck,
	KeyStatusNotifyCount,
	KeyStatusNotifyAgeCompact,
	KeySettingsRootNotifications,
	KeySettingsRootProject,
	KeySettingsRootAppearance,
	KeySettingsNotificationsDesktop,
	KeySettingsNotificationsDelivery,
	KeySettingsAboutWelcome,
	KeyPickerPromptSearch,
	KeyPickerFooterOpenRows,
	KeyPickerFooterBack,
	KeyPickerFooterClose,
	KeyPickerEmptyNoMatches,
	KeyWelcomeShellTitle,
	KeyUpdateStatusAvailable,
	KeyHelpUsageCommand,
}

// FoundationKeys returns the Phase 0 draft key set that Phase 1 must cover.
func FoundationKeys() []Key {
	return append([]Key(nil), foundationKeys...)
}

// MessageKind separates plain text from terminal-styled fragments.
type MessageKind string

const (
	MessageKindText MessageKind = "text"
	MessageKindANSI MessageKind = "ansi"
	MessageKindTmux MessageKind = "tmux"
)

// Entry is one catalog value. Most Phase 1 keys are plain text; styled entries
// are modeled now so future renderers request the right type deliberately.
type Entry struct {
	Kind  MessageKind
	Value string
}

// Text is a plain message without ANSI or tmux style syntax.
type Text struct {
	key    Key
	locale Locale
	value  string
}

func (t Text) Key() Key       { return t.key }
func (t Text) Locale() Locale { return t.locale }
func (t Text) String() string { return t.value }

// StyledFragment is a message containing terminal styling syntax.
type StyledFragment struct {
	key    Key
	locale Locale
	kind   MessageKind
	value  string
}

func (f StyledFragment) Key() Key           { return f.key }
func (f StyledFragment) Locale() Locale     { return f.locale }
func (f StyledFragment) Kind() MessageKind  { return f.kind }
func (f StyledFragment) String() string     { return f.value }
func (f StyledFragment) IsANSI() bool       { return f.kind == MessageKindANSI }
func (f StyledFragment) IsTmuxStyle() bool  { return f.kind == MessageKindTmux }
func (f StyledFragment) IsStyledKind() bool { return f.IsANSI() || f.IsTmuxStyle() }

// Catalog is an immutable in-memory message catalog.
type Catalog struct {
	locales map[Locale]map[Key]Entry
}

// NewCatalog copies locale data into an immutable catalog value.
func NewCatalog(locales map[Locale]map[Key]Entry) Catalog {
	copied := make(map[Locale]map[Key]Entry, len(locales))
	for locale, entries := range locales {
		copiedEntries := make(map[Key]Entry, len(entries))
		maps.Copy(copiedEntries, entries)
		copied[locale] = copiedEntries
	}
	return Catalog{locales: copied}
}

// DefaultCatalog returns the embedded projmux Phase 1 catalog.
func DefaultCatalog() Catalog {
	return NewCatalog(defaultCatalogData)
}

// MissingFallbackKeys reports required keys absent from the fallback locale.
func (c Catalog) MissingFallbackKeys(required []Key) []Key {
	entries := c.locales[FallbackLocale]
	var missing []Key
	for _, key := range required {
		entry, ok := entries[key]
		if !ok || entry.Value == "" {
			missing = append(missing, key)
		}
	}
	return missing
}

// Localizer resolves keys for one preferred locale with en-US fallback.
type Localizer struct {
	catalog Catalog
	locale  Locale
}

// NewLocalizer creates a localizer against the embedded catalog.
func NewLocalizer(locale Locale) Localizer {
	return NewLocalizerWithCatalog(DefaultCatalog(), locale)
}

// NewLocalizerWithCatalog creates a localizer against a caller-supplied catalog.
func NewLocalizerWithCatalog(catalog Catalog, locale Locale) Localizer {
	if locale == "" {
		locale = FallbackLocale
	}
	return Localizer{catalog: catalog, locale: locale}
}

// Locale returns the localizer's preferred locale.
func (l Localizer) Locale() Locale {
	return l.locale
}

// Text returns a plain text message, falling back to en-US when needed.
func (l Localizer) Text(key Key) (Text, error) {
	entry, locale, err := l.lookup(key)
	if err != nil {
		return Text{}, err
	}
	if entry.Kind != MessageKindText {
		return Text{}, KindMismatchError{Key: key, Locale: locale, Want: string(MessageKindText), Got: entry.Kind}
	}
	return Text{key: key, locale: locale, value: entry.Value}, nil
}

// Styled returns an ANSI or tmux styled fragment, falling back to en-US when needed.
func (l Localizer) Styled(key Key) (StyledFragment, error) {
	entry, locale, err := l.lookup(key)
	if err != nil {
		return StyledFragment{}, err
	}
	if entry.Kind != MessageKindANSI && entry.Kind != MessageKindTmux {
		return StyledFragment{}, KindMismatchError{Key: key, Locale: locale, Want: "ansi/tmux styled fragment", Got: entry.Kind}
	}
	return StyledFragment{key: key, locale: locale, kind: entry.Kind, value: entry.Value}, nil
}

func (l Localizer) lookup(key Key) (Entry, Locale, error) {
	if entry, ok := l.lookupLocale(l.locale, key); ok {
		return entry, l.locale, nil
	}
	if l.locale != FallbackLocale {
		if entry, ok := l.lookupLocale(FallbackLocale, key); ok {
			return entry, FallbackLocale, nil
		}
	}
	return Entry{}, "", MissingKeyError{Key: key, Locale: l.locale, FallbackLocale: FallbackLocale}
}

func (l Localizer) lookupLocale(locale Locale, key Key) (Entry, bool) {
	entries, ok := l.catalog.locales[locale]
	if !ok {
		return Entry{}, false
	}
	entry, ok := entries[key]
	if !ok || entry.Value == "" {
		return Entry{}, false
	}
	if entry.Kind == "" {
		entry.Kind = MessageKindText
	}
	return entry, true
}

// MissingKeyError is returned when neither the preferred locale nor en-US has a key.
type MissingKeyError struct {
	Key            Key
	Locale         Locale
	FallbackLocale Locale
}

func (e MissingKeyError) Error() string {
	return fmt.Sprintf("i18n: missing key %q for locale %q and fallback %q", e.Key, e.Locale, e.FallbackLocale)
}

// KindMismatchError is returned when a caller requests text as styled or styled as text.
type KindMismatchError struct {
	Key    Key
	Locale Locale
	Want   string
	Got    MessageKind
}

func (e KindMismatchError) Error() string {
	return fmt.Sprintf("i18n: key %q for locale %q has kind %q, want %q", e.Key, e.Locale, e.Got, e.Want)
}
