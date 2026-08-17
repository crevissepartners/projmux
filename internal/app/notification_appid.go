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
//
// Legacy: retained for Windows AppUserModelID migration and idempotent stale
// marker cleanup; sunset when a post-0.7 review after two minor releases or 90
// days confirms stale tmux markers from pre-cleanup installs are no longer
// supported.

const (
	desktopAppID       = "com.crevisse.projmux"
	desktopDisplayName = "projmux"
	legacyDesktopAppID = "projmux.TmuxCodex"

	// legacyAppIDCleanedTmuxOption marks that the one-shot Windows
	// legacy AppID cleanup has already been attempted on this server.
	// Stored as a global tmux user-option so the marker survives across
	// sessions on the same tmux server.
	legacyAppIDCleanedTmuxOption = "@projmux_legacy_appid_cleaned"

	// desktopURIScheme is the scheme accepted by `projmux internal focus --uri`
	// compatibility entrypoint. projmux no longer registers a Windows
	// protocol handler for it and no longer emits clickable Toasts, so
	// nothing in the product produces such a URI; the scheme is retained
	// only so an externally-wired handler that predates 0.11.0 still parses.
	desktopURIScheme = "projmux"
)
