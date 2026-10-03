package i18n

import "testing"

func TestProcessAttentionCatalogParity(t *testing.T) {
	for _, test := range []struct {
		key             Key
		english, korean string
	}{
		{"notify.process.ready", "Ready", "준비됨"},
		{"notify.process.input_required", "Input required", "입력 필요"},
		{"notify.process.approval_required", "Approval required", "승인 필요"},
		{"notify.process.error", "Process attention error", "프로세스 알림 오류"},
	} {
		for _, locale := range []Locale{FallbackLocale, "ko-KR"} {
			text, err := NewLocalizer(locale).Text(test.key)
			want := test.english
			if locale == "ko-KR" {
				want = test.korean
			}
			if err != nil || text.Locale() != locale || text.String() != want {
				t.Errorf("%s/%s: text=%+v err=%v want=%q", locale, test.key, text, err, want)
			}
		}
	}
}
