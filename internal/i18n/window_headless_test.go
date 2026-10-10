package i18n

import "testing"

func TestWindowHeadlessCatalogParity(t *testing.T) {
	for _, test := range []struct {
		locale Locale
		want   string
	}{{FallbackLocale, "headless"}, {"ko-KR", "헤드리스"}} {
		text, err := NewLocalizer(test.locale).Text(KeyWindowHeadless)
		if err != nil || text.Locale() != test.locale || text.String() != test.want {
			t.Fatalf("%s: %+v %v", test.locale, text, err)
		}
	}
}

func TestWindowOpenRequiresClientCatalogParity(t *testing.T) {
	for _, test := range []struct {
		locale Locale
		want   string
	}{{FallbackLocale, "Open requires a tmux client; enter the Project first"}, {"ko-KR", "Open은 tmux 클라이언트가 필요합니다. 먼저 Project에 들어가세요"}} {
		text, err := NewLocalizer(test.locale).Text(KeyWindowOpenRequiresClient)
		if err != nil || text.Locale() != test.locale || text.String() != test.want {
			t.Fatalf("%s: %+v %v", test.locale, text, err)
		}
	}
}
