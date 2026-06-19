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

	// desktopURIScheme is the custom URI scheme the Windows side registers
	// so that a Toast click can hand control back to projmux running inside
	// WSL. The scheme is shared across all environments — only WSL +
	// Windows Terminal users actually register the handler today (other
	// platforms keep the in-app focus path). The handler command we register
	// launches a GUI-subsystem WScript wrapper which then runs
	// `wsl.exe -d <distro> --exec <abs-binary-path> focus --uri <uri>`.
	// `--exec` bypasses the user's login shell so URI query separators (`&`)
	// don't get parsed as background-job operators by zsh/bash.
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
	// users get a fresh registration on their next Notify dispatch.
	//
	// v3 — incremented after the handler moved from direct `wsl.exe` launch to
	// a hidden PowerShell wrapper to avoid the visible Windows console flash on
	// Toast click.
	//
	// v4 — incremented after the hidden wrapper stopped passing `%1` after
	// `-Command`, which PowerShell parsed as command text and broke URI query
	// separators. Existing v3 markers are left orphaned so affected servers
	// install the ProcessStartInfo/CreateNoWindow wrapper on their next Notify.
	//
	// v5 — incremented after the first process in the protocol command moved
	// from console-subsystem PowerShell to GUI-subsystem WScript because
	// PowerShell itself could still flash before hiding its window.
	//
	// v6 — incremented after the WScript launcher added a hidden cmd.exe
	// command-line parser hop. WScript.Shell.Run passed quoted wsl.exe
	// arguments literally enough to break focus; cmd.exe strips the caret
	// escapes around URI `&` separators before wsl.exe receives the argv.
	uriProtocolRegisteredTmuxOption = "@projmux_uri_protocol_registered_v6"
)

var legacyURIProtocolRegisteredTmuxOptions = []string{
	"@projmux_uri_protocol_registered",
	"@projmux_uri_protocol_registered_v2",
	"@projmux_uri_protocol_registered_v3",
	"@projmux_uri_protocol_registered_v4",
	"@projmux_uri_protocol_registered_v5",
}
