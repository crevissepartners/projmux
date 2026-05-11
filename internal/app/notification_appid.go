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

	// desktopURIScheme is the custom URI scheme the Windows side registers
	// so that a Toast click can hand control back to projmux running inside
	// WSL. The scheme is shared across all environments — only WSL +
	// Windows Terminal users actually register the handler today (other
	// platforms keep the in-app focus path). The handler command we register
	// looks like `wsl.exe -d <distro> --exec <abs-binary-path> focus --uri "%1"`
	// — `--exec` bypasses the user's login shell so URI query separators
	// (`&`) don't get parsed as background-job operators by zsh/bash.
	//
	// This implements the "(a) on-push (자동)" trigger mode from the
	// roadmap detail (Notify 시 터미널 OS 포커스) by piping the user's
	// click on the system notification through the existing (b)
	// `projmux focus` path. See docs/notify-os-focus-poc.md for the spike
	// trail.
	desktopURIScheme = "projmux"

	// uriProtocolRegisteredTmuxOption marks that the one-shot URI protocol
	// registration has already been attempted on this server. Same shape
	// and rationale as legacyAppIDCleanedTmuxOption: we register at most
	// once per tmux server lifetime to keep the notify path fast.
	//
	// v2 — incremented after the hot-fix that switched the registry command
	// from `wsl.exe -- projmux ...` (broke on `&` due to shell
	// interpretation) to `wsl.exe --exec <abs-path> ...`. Existing v1 marker
	// users get a fresh registration on their next Notify dispatch; the v1
	// key (`@projmux_uri_protocol_registered`) is left orphaned —
	// re-registration is idempotent so no cleanup is needed.
	uriProtocolRegisteredTmuxOption = "@projmux_uri_protocol_registered_v2"
)
