package app

// notification_appid.go centralizes the desktop notification AppID brand.
// Phase 2 of the OS-알림 정리 roadmap migrated the historical
// `projmux.TmuxCodex` label to a reverse-domain id that Windows
// AppUserModelID rules accept natively and macOS / Linux render cleanly.
//
// `legacyDesktopAppID` is retained for two reasons:
//  1. The WSL Toast register helper sanity-guards against unknown app
//     ids; the legacy value stays whitelisted so users mid-migration
//     keep getting toasts.
//  2. The one-shot Windows cleanup helper removes the Start Menu
//     shortcut and the AppUserModelID registry key tied to the legacy
//     value. Keeping the constant alongside the new one makes the
//     migration footprint discoverable in one place.

const (
	desktopAppID       = "com.crevisse.projmux"
	desktopDisplayName = "projmux"
	legacyDesktopAppID = "projmux.TmuxCodex"

	// legacyAppIDCleanedTmuxOption marks that the one-shot Windows
	// legacy AppID cleanup has already been attempted on this server.
	// Stored as a global tmux user-option so the marker survives across
	// sessions on the same tmux server.
	legacyAppIDCleanedTmuxOption = "@projmux_legacy_appid_cleaned"
)
