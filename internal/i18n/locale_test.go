package i18n

import "testing"

func TestResolveLocalePriority(t *testing.T) {
	env := map[string]string{
		"LC_ALL":      "ja_JP.UTF-8",
		"LC_MESSAGES": "ko_KR.UTF-8",
		"LANG":        "en_US.UTF-8",
	}
	lookup := func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	}

	got := ResolveLocale(LocaleOptions{Override: "ko", LookupEnv: lookup})
	if got.Locale != Locale("ko-KR") || got.Source != LocaleSourceOverride {
		t.Fatalf("override resolution = %+v, want ko-KR from override", got)
	}

	got = ResolveLocale(LocaleOptions{LookupEnv: lookup})
	if got.Locale != Locale("ja-JP") || got.Source != LocaleSourceLCAll {
		t.Fatalf("LC_ALL resolution = %+v, want ja-JP from LC_ALL", got)
	}

	delete(env, "LC_ALL")
	got = ResolveLocale(LocaleOptions{LookupEnv: lookup})
	if got.Locale != Locale("ko-KR") || got.Source != LocaleSourceLCMessages {
		t.Fatalf("LC_MESSAGES resolution = %+v, want ko-KR from LC_MESSAGES", got)
	}

	delete(env, "LC_MESSAGES")
	got = ResolveLocale(LocaleOptions{LookupEnv: lookup})
	if got.Locale != FallbackLocale || got.Source != LocaleSourceLANG {
		t.Fatalf("LANG resolution = %+v, want en-US from LANG", got)
	}

	delete(env, "LANG")
	got = ResolveLocale(LocaleOptions{LookupEnv: lookup})
	if got.Locale != FallbackLocale || got.Source != LocaleSourceFallback {
		t.Fatalf("fallback resolution = %+v, want en-US fallback", got)
	}
}

func TestResolveLocaleDefaultLookupUsesEnvironment(t *testing.T) {
	t.Setenv("LC_ALL", "ko_KR.UTF-8")
	got := ResolveLocale(LocaleOptions{})
	if got.Locale != Locale("ko-KR") || got.Source != LocaleSourceLCAll {
		t.Fatalf("default lookup resolution = %+v, want ko-KR from LC_ALL", got)
	}
}

func TestResolveLocaleSkipsPOSIXLocale(t *testing.T) {
	env := map[string]string{
		"LC_ALL":      "C.UTF-8",
		"LC_MESSAGES": "POSIX",
		"LANG":        "ko_KR.UTF-8",
	}
	got := ResolveLocale(LocaleOptions{LookupEnv: func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	}})
	if got.Locale != Locale("ko-KR") || got.Source != LocaleSourceLANG {
		t.Fatalf("resolution = %+v, want ko-KR from LANG", got)
	}
}

func TestNormalizeLocale(t *testing.T) {
	tests := map[string]Locale{
		"ko":             "ko-KR",
		"ko_KR.UTF-8":    "ko-KR",
		"ko-KR":          "ko-KR",
		"en":             "en-US",
		"en_US":          "en-US",
		"en-US.UTF-8":    "en-US",
		"ja_JP.UTF-8":    "ja-JP",
		"pt_BR@modifier": "pt-BR",
	}
	for raw, want := range tests {
		got, ok := NormalizeLocale(raw)
		if !ok {
			t.Fatalf("NormalizeLocale(%q) returned !ok", raw)
		}
		if got != want {
			t.Fatalf("NormalizeLocale(%q) = %q, want %q", raw, got, want)
		}
	}
}
