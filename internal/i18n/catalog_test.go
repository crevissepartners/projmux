package i18n

import (
	"errors"
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
