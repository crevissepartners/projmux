package i18n

import (
	"errors"
	"strings"
	"testing"
)

func TestKoKRFallsBackToEnUSForMissingKey(t *testing.T) {
	localizer := NewLocalizer(Locale("ko-KR"))
	got, err := localizer.Text(KeyNotifyQueueActionAck)
	if err != nil {
		t.Fatalf("Text returned error: %v", err)
	}
	if got.Locale() != FallbackLocale {
		t.Fatalf("fallback locale = %q, want %q", got.Locale(), FallbackLocale)
	}
	if got.String() != "ack" {
		t.Fatalf("fallback text = %q, want ack", got.String())
	}
}

func TestFallbackMissingKeyFails(t *testing.T) {
	catalog := NewCatalog(map[Locale]map[Key]Entry{
		FallbackLocale: {},
		Locale("ko-KR"): {
			KeyNotifyAIResponseComplete: textEntry("응답 완료"),
		},
	})
	localizer := NewLocalizerWithCatalog(catalog, Locale("ko-KR"))
	_, err := localizer.Text(KeyNotifyAIApprovalRequired)
	if err == nil {
		t.Fatal("Text returned nil error, want MissingKeyError")
	}
	var missing MissingKeyError
	if !errors.As(err, &missing) {
		t.Fatalf("error = %T %v, want MissingKeyError", err, err)
	}
	if missing.Key != KeyNotifyAIApprovalRequired || missing.FallbackLocale != FallbackLocale {
		t.Fatalf("missing key error = %+v", missing)
	}
}

func TestDefaultCatalogEnUSCompletesFoundationKeys(t *testing.T) {
	missing := DefaultCatalog().MissingFallbackKeys(FoundationKeys())
	if len(missing) > 0 {
		t.Fatalf("en-US catalog missing foundation keys: %v", missing)
	}
}

func TestDefaultCatalogEnUSCompletesAllDefaultCatalogKeys(t *testing.T) {
	required := DefaultCatalog().Keys()
	missing := DefaultCatalog().MissingLocaleKeys(FallbackLocale, required)
	if len(missing) > 0 {
		t.Fatalf("en-US catalog missing default catalog keys: %v", missing)
	}
}

func TestDefaultCatalogKoKRCompletesRequiredMigratedSurfaces(t *testing.T) {
	required := requiredDefaultCatalogKeysByPrefix(DefaultCatalog(), []string{
		"notify.ai.",
		"notify.live.",
		"settings.",
		"picker.",
		"welcome.",
		"update.",
		"help.",
	})
	missing := DefaultCatalog().MissingLocaleKeys(Locale("ko-KR"), required)
	if len(missing) > 0 {
		t.Fatalf("ko-KR catalog missing required migrated surface keys: %v", missing)
	}
}

func TestDefaultCatalogAIResumeDetailLabels(t *testing.T) {
	tests := []struct {
		locale Locale
		key    Key
		want   string
	}{
		{FallbackLocale, KeyPickerResumeDetailHelp, "Select a resume session to see details."},
		{FallbackLocale, KeyPickerResumeDetailTurns, "Turns"},
		{FallbackLocale, KeyPickerResumeDetailConfidence, "Confidence"},
		{FallbackLocale, KeyPickerResumeDetailReason, "Reason"},
		{Locale("ko-KR"), KeyPickerResumeDetailHelp, "재개 세션을 선택하면 상세 정보를 볼 수 있습니다."},
		{Locale("ko-KR"), KeyPickerResumeDetailTurns, "턴"},
		{Locale("ko-KR"), KeyPickerResumeDetailConfidence, "신뢰도"},
		{Locale("ko-KR"), KeyPickerResumeDetailReason, "사유"},
	}
	for _, test := range tests {
		text, err := NewLocalizer(test.locale).Text(test.key)
		if err != nil {
			t.Fatalf("Text(%q, %q) error = %v", test.locale, test.key, err)
		}
		if got := text.String(); got != test.want {
			t.Fatalf("Text(%q, %q) = %q, want %q", test.locale, test.key, got, test.want)
		}
	}
}

func TestTextRejectsStyledFragment(t *testing.T) {
	key := Key("test.styled")
	catalog := NewCatalog(map[Locale]map[Key]Entry{
		FallbackLocale: {
			key: {Kind: MessageKindTmux, Value: "#[bold]Styled"},
		},
	})
	localizer := NewLocalizerWithCatalog(catalog, FallbackLocale)
	_, err := localizer.Text(key)
	if err == nil {
		t.Fatal("Text returned nil error, want KindMismatchError")
	}
	var mismatch KindMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("error = %T %v, want KindMismatchError", err, err)
	}
	if mismatch.Want != string(MessageKindText) {
		t.Fatalf("mismatch.Want = %q, want %q", mismatch.Want, MessageKindText)
	}
}

func TestStyledRejectsPlainText(t *testing.T) {
	localizer := NewLocalizer(FallbackLocale)
	_, err := localizer.Styled(KeyNotifyAIResponseComplete)
	if err == nil {
		t.Fatal("Styled returned nil error, want KindMismatchError")
	}
	var mismatch KindMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("error = %T %v, want KindMismatchError", err, err)
	}
	if mismatch.Want != "ansi/tmux styled fragment" {
		t.Fatalf("mismatch.Want = %q, want styled fragment description", mismatch.Want)
	}
}

func requiredDefaultCatalogKeysByPrefix(catalog Catalog, prefixes []string) []Key {
	var keys []Key
	for _, key := range catalog.LocaleKeys(FallbackLocale) {
		for _, prefix := range prefixes {
			if strings.HasPrefix(string(key), prefix) {
				keys = append(keys, key)
				break
			}
		}
	}
	return keys
}

// TestDefaultCatalogManagedDeleteConfirmParity pins the confirmation prompt the
// managed tmux close keys show. Both locales must name the Registry deletion
// and the Agent consequence -- deleted, and not coming back on Continue -- and
// keep tmux's own target format plus the y/n key hint the prompt answers.
func TestDefaultCatalogManagedDeleteConfirmParity(t *testing.T) {
	for _, test := range []struct {
		key    Key
		format string
		want   map[Locale][]string
	}{
		{
			key:    Key("tmux.confirm.delete_pane"),
			format: "#P",
			want: map[Locale][]string{
				FallbackLocale:  {"Delete Pane", "Registry", "Agent", "deleted", "Continue"},
				Locale("ko-KR"): {"Pane", "Registry", "삭제", "Agent", "Continue", "돌아오지 않습니다"},
			},
		},
		{
			key:    Key("tmux.confirm.delete_window"),
			format: "#W",
			want: map[Locale][]string{
				FallbackLocale:  {"Delete Window", "Panes", "Registry", "Agents", "deleted", "Continue"},
				Locale("ko-KR"): {"Window", "Pane", "Registry", "삭제", "Agent", "Continue", "돌아오지 않습니다"},
			},
		},
	} {
		for _, locale := range []Locale{FallbackLocale, Locale("ko-KR")} {
			missing := DefaultCatalog().MissingLocaleKeys(locale, []Key{test.key})
			if len(missing) != 0 {
				t.Fatalf("%s catalog is missing %s; the managed delete prompt must not fall back across locales", locale, test.key)
			}
			text, err := NewLocalizer(locale).Text(test.key)
			if err != nil {
				t.Fatalf("Text(%s, %s) error = %v", locale, test.key, err)
			}
			value := text.String()
			if text.Locale() != locale {
				t.Fatalf("Text(%s, %s) resolved from %s", locale, test.key, text.Locale())
			}
			for _, want := range append([]string{test.format, "(y/n)"}, test.want[locale]...) {
				if !strings.Contains(value, want) {
					t.Fatalf("%s %s = %q, want it to contain %q", locale, test.key, value, want)
				}
			}
			if strings.Contains(value, "%") || strings.Contains(value, "\n") {
				t.Fatalf("%s %s = %q; a tmux prompt must stay one line without printf verbs", locale, test.key, value)
			}
		}
	}
}
