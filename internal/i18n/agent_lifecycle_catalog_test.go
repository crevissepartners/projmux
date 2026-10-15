package i18n

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestAgentLifecycleCatalogLocaleAndArgumentParity(t *testing.T) {
	if err := validateCatalogFragment(agentLifecycleCatalog); err != nil {
		t.Fatal(err)
	}
	formats := regexp.MustCompile(`%[sd]`)
	en := agentLifecycleCatalog.locales[FallbackLocale]
	ko := agentLifecycleCatalog.locales[Locale("ko-KR")]
	for key, e := range en {
		other, ok := ko[key]
		if !ok || !strings.HasPrefix(string(key), "agent.lifecycle.") {
			t.Fatal(key)
		}
		if !slices.Equal(formats.FindAllString(e.Value, -1), formats.FindAllString(other.Value, -1)) {
			t.Fatalf("format arguments differ for %s", key)
		}
		for _, locale := range []Locale{FallbackLocale, Locale("ko-KR")} {
			text, err := NewLocalizer(locale).Text(key)
			if err != nil || text.String() == "" || text.Locale() != locale {
				t.Fatalf("%s %s: %v", locale, key, err)
			}
		}
		text, err := NewLocalizer(Locale("unsupported")).Text(key)
		if err != nil || text.String() != e.Value || text.Locale() != FallbackLocale {
			t.Fatalf("fallback %s: %v", key, err)
		}
	}
	for _, name := range []string{"profile", "model", "effort", "instructions", "agent-guidance", "project-guidance", "link-rules", "workspace"} {
		if _, ok := en[Key("agent.lifecycle.setting."+name)]; !ok {
			t.Fatal(name)
		}
	}
	for _, reason := range []string{"launch-arguments", "thread-settings-ready", "profile-recipe-recorded", "first-input-required", "conversation-model-preserved", "recorded-snapshot", "codex-instructions-immutable", "instructions-unavailable", "effort-invalid", "guidance-unavailable", "project-guidance-unavailable", "link-rules-unavailable", "workspace-resolved", "application-unobserved"} {
		if _, ok := en[Key("agent.lifecycle.reason."+reason)]; !ok {
			t.Fatal(reason)
		}
	}
	for _, key := range []string{"help.resume", "help.relaunch", "help.application", "help.mapping", "help.lifetime", "help.model", "prompt", "foreground", "summary", "missing", "pending", "claim-waiting", "delivered-entry"} {
		if _, ok := en[Key("agent.lifecycle."+key)]; !ok {
			t.Fatal(key)
		}
	}
}
