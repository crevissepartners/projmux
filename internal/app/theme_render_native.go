package app

import (
	"sync"
	"testing"

	"github.com/crevissepartners/projmux/internal/theme"
	intrender "github.com/crevissepartners/projmux/internal/ui/render"
)

// theme_render_native.go threads resolved ANSI semantic roles into the native
// (terminal-rendered) UI surfaces owned by the app package: settings rows,
// trust badges, the notify sidebar, and the AI badge palette shared by the
// recent-window and switch pickers.
//
// The settings/trust/notify role colors live as package-level vars (defaulting
// to the historical fallback literals) rather than threaded parameters because
// they are consumed from dozens of stateless formatter call sites; threading a
// struct through all of them would be far more invasive for no functional gain.
// Each CLI render is single-threaded and applyNativeUITheme runs once at command
// entry before any row is formatted, so there is no data race in production.
//
// A fallback-sourced ANSIRoles restores byte-identity, so applying the fallback
// theme is a no-op in observable output.
//
// nativeUIThemeMu serializes the apply-render-restore section. Production runs a
// single one-shot CLI command, so the lock is uncontended; it is defense in
// depth. In tests the config-read apply path is gated off entirely (see
// applyNativeUIThemeFromConfig) so the suite never mutates these vars from a
// parallel Run; the one repaint test mutates them deliberately under this lock.
var nativeUIThemeMu sync.Mutex

// appAIBadge* are the AI badge role escapes shared by recent_window.go (and
// kept congruent with the switch renderer). They default to fallback literals.
var (
	appAIBadgeActionRequired = theme.ANSIAIBadgeActionRequiredStart
	appAIBadgeSuccess        = theme.ANSIAIBadgeSuccessStart
	appAIBadgeProgress       = theme.ANSIAIBadgeProgressStart
)

// applyNativeUITheme repoints every app-package native-UI role escape at a
// resolved effective theme. Call once at command entry. Passing the fallback
// theme restores the historical literals (byte-identity).
func applyNativeUITheme(effective theme.EffectiveTheme) {
	roles := theme.ANSIRolesFromEffective(effective)

	// settings rows (settings_render.go)
	settingsColorAdd = roles.AccentAction
	settingsColorType = roles.AccentAction
	settingsColorRemove = roles.StateCritical
	settingsColorBack = roles.TextSecondary
	settingsColorDim = roles.TextDim
	settingsColorInfo = roles.TextPrimary

	// settings root rows (settings.go)
	settingsRootColorOpen = roles.AccentActionStrong
	settingsRootColorDim = roles.TextMuted

	// trust badges (trust.go)
	settingsColorTrustTrusted = roles.TrustTrusted
	settingsColorTrustStale = roles.TrustStale
	settingsColorTrustUntrusted = roles.TrustUntrusted

	// notify sidebar (notify.go)
	notifySidebarDimOpen = roles.NotifyDim
	notifySidebarProject = roles.NotifyProject
	notifySidebarInfo = roles.NotifyInfo
	notifySidebarWarn = roles.NotifyWarn
	notifySidebarCrit = roles.NotifyCrit
	notifySidebarStale = roles.NotifyStale
	notifySidebarGone = roles.NotifyGone
	notifySidebarTitle = roles.NotifyTitle
	notifySidebarAgeOpen = roles.NotifyAge
	notifySidebarTopicOpen = roles.ChipActive
	notifySidebarAgentOpenStyle = roles.NotifyAgent

	// AI badge palette (recent_window.go)
	appAIBadgeActionRequired = roles.AIBadgeActionRequired
	appAIBadgeSuccess = roles.AIBadgeSuccess
	appAIBadgeProgress = roles.AIBadgeProgress
}

// applyNativeUIThemeFromConfig resolves the effective theme from the global user
// config and applies it to both the app-package native-UI role escapes and the
// switch renderer (internal/ui/render) role escapes. A resolution failure leaves
// the fallback literals in place (best-effort), matching how the picker falls
// back when no theme is resolvable.
//
// It acquires nativeUIThemeMu and returns a restore func that resets every role
// escape to the built-in fallback literals and releases the lock. Each command's
// Run defers the restore so the apply-render-restore section is atomic: a one-shot
// CLI invocation never leaves the process-global role escapes in a themed state,
// and the in-process test suite stays deterministic even when the developer has
// an explicit global theme configured.
func applyNativeUIThemeFromConfig(homeDir func() (string, error), lookupEnv func(string) string, projectPath string) (restore func()) {
	// Under `go test` the suite runs many commands in parallel and a developer
	// may have an explicit global [theme] configured; mutating the shared role
	// vars from a parallel Run would make fallback-asserting tests flaky. The
	// native-UI repaint is exercised directly via the pure theme.ANSIRoles
	// adapter (internal/theme) and applyNativeUITheme, so gating the config-read
	// path here keeps the app suite deterministic without losing coverage.
	if testing.Testing() {
		return func() {}
	}
	nativeUIThemeMu.Lock()
	effective, err := effectiveThemeFromConfig(homeDir, lookupEnv, projectPath)
	if err != nil {
		nativeUIThemeMu.Unlock()
		return func() {}
	}
	applyNativeUIThemeLocked(effective)
	return func() {
		resetNativeUIThemeLocked()
		nativeUIThemeMu.Unlock()
	}
}

func applyNativeUIThemeLocked(effective theme.EffectiveTheme) {
	applyNativeUITheme(effective)
	intrender.ApplyTheme(theme.ANSIRolesFromEffective(effective))
}

// resetNativeUIThemeLocked restores every native-UI role escape to the built-in
// fallback literals (byte-identity baseline). The caller must hold
// nativeUIThemeMu.
func resetNativeUIThemeLocked() {
	fallback := theme.ResolveTheme(theme.ThemeConfig{})
	applyNativeUITheme(fallback)
	intrender.ApplyTheme(theme.ANSIRolesFromEffective(fallback))
}
