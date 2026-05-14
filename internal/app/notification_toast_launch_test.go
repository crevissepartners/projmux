package app

import (
	"strings"
	"testing"
)

func TestBuildToastPowerShell_OmitsLaunchWhenURIEmpty(t *testing.T) {
	t.Parallel()

	script := buildToastPowerShell("hello", "world", "com.crevisse.projmux", "%8", "session", "", "", defaultAINotifyExpireMS)
	if strings.Contains(script, "launch=") {
		t.Fatalf("expected launch attribute absent when launchURI is empty; got:\n%s", script)
	}
	if strings.Contains(script, "activationType=") {
		t.Fatalf("expected activationType attribute absent when launchURI is empty; got:\n%s", script)
	}
	if !strings.Contains(script, `<toast duration="short">`) {
		t.Fatalf("expected passive short-duration <toast> open tag without launch URI; got:\n%s", script)
	}
	if !strings.Contains(script, "$toast.ExpirationTime = [DateTimeOffset]::Now.AddMilliseconds(5000)") {
		t.Fatalf("expected explicit toast expiration; got:\n%s", script)
	}
}

func TestBuildToastPowerShell_InjectsLaunchAndProtocolActivation(t *testing.T) {
	t.Parallel()

	uri := buildFocusURI("%8", "/tmp/tmux-1000/projmux")
	script := buildToastPowerShell("hello", "world", "com.crevisse.projmux", "%8", "session", "", uri, 2500)
	if !strings.Contains(script, `activationType="protocol"`) {
		t.Fatalf("expected activationType=\"protocol\"; got:\n%s", script)
	}
	if !strings.Contains(script, `<toast duration="short" launch="`) {
		t.Fatalf("expected launch URI on short-duration toast; got:\n%s", script)
	}
	if !strings.Contains(script, "$toast.ExpirationTime = [DateTimeOffset]::Now.AddMilliseconds(2500)") {
		t.Fatalf("expected configured toast expiration; got:\n%s", script)
	}
	if !strings.Contains(script, `launch="`) {
		t.Fatalf("expected launch attribute present; got:\n%s", script)
	}
	// The URI is URL-encoded once by buildFocusURI; the XML layer then
	// xml-escapes `&` to `&amp;`. The two encodings compose, so the raw
	// URI's `&` must appear in the script as `&amp;`.
	if !strings.Contains(script, "&amp;") {
		t.Fatalf("expected `&` in URI to be xml-escaped to `&amp;`; got:\n%s", script)
	}
	if strings.Contains(script, "?pane_id=%8&socket=") {
		t.Fatalf("raw `&` leaked into XML attribute (broken double-encoding); got:\n%s", script)
	}
}

func TestBuildRegisterURIProtocolPowerShell_WritesAllRegistryKeys(t *testing.T) {
	t.Parallel()

	script := buildRegisterURIProtocolPowerShell("projmux", "Ubuntu-24.04", "/home/me/go/bin/projmux")
	wantSubstrings := []string{
		`$regPath = "HKCU:\SOFTWARE\Classes\projmux"`,
		`$cmdPath = "$regPath\shell\open\command"`,
		"New-Item -Path $regPath -Force",
		"New-Item -Path $cmdPath -Force",
		`Set-ItemProperty -Path $regPath -Name '(Default)' -Value 'URL:projmux'`,
		"Set-ItemProperty -Path $regPath -Name 'URL Protocol' -Value ''",
		`powershell.exe -NoProfile -WindowStyle Hidden -ExecutionPolicy Bypass`,
		`Start-Process -WindowStyle Hidden -FilePath ''wsl.exe''`,
		`Set-ItemProperty -Path $cmdPath -Name '(Default)'`,
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(script, want) {
			t.Fatalf("register script missing %q:\n%s", want, script)
		}
	}
}

func TestBuildRegisterURIProtocolPowerShell_UsesHiddenLauncherNotDirectWSLCommand(t *testing.T) {
	t.Parallel()

	script := buildRegisterURIProtocolPowerShell("projmux", "Ubuntu-24.04", "/home/me/go/bin/projmux")
	direct := `wsl.exe -d Ubuntu-24.04 --exec /home/me/go/bin/projmux focus --uri "%1"`
	if strings.Contains(script, direct) {
		t.Fatalf("script must not register direct wsl.exe protocol command %q:\n%s", direct, script)
	}
	for _, want := range []string{
		`powershell.exe -NoProfile -WindowStyle Hidden -ExecutionPolicy Bypass`,
		`Start-Process -WindowStyle Hidden -FilePath ''wsl.exe''`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing hidden launcher intent %q:\n%s", want, script)
		}
	}
}

func TestBuildWSLURIProtocolHandlerCommand_BypassesShellWithExecAndAbsolutePath(t *testing.T) {
	t.Parallel()

	// Regression guard: the registered launch command must use `--exec`
	// (skips the user's login shell so URI query `&` separators don't get
	// parsed as background-job operators by zsh/bash) and must point at the
	// absolute WSL filesystem path to the projmux binary (because `--exec`
	// doesn't load shell init files, so PATH is unreliable). The bare
	// `-- projmux focus` form shipped in PR #178 broke on the very first
	// `&`-bearing toast click; do not let it come back.
	command := buildWSLURIProtocolHandlerCommand("Ubuntu-24.04", "/home/me/go/bin/projmux")
	wantLaunch := `Start-Process -WindowStyle Hidden -FilePath 'wsl.exe' -ArgumentList @('-d', 'Ubuntu-24.04', '--exec', '/home/me/go/bin/projmux', 'focus', '--uri', $uri)`
	if !strings.Contains(command, wantLaunch) {
		t.Fatalf("expected hidden launch command %q in command:\n%s", wantLaunch, command)
	}
	if strings.Contains(command, `-- projmux focus`) {
		t.Fatalf("command must not use the legacy `-- projmux focus` form (shell-interpreted, breaks on `&`):\n%s", command)
	}
	if !strings.Contains(command, "'--exec'") {
		t.Fatalf("command must use `--exec` to bypass the login shell:\n%s", command)
	}
}

func TestBuildWSLURIProtocolHandlerCommand_ForwardsURIAsArgument(t *testing.T) {
	t.Parallel()

	command := buildWSLURIProtocolHandlerCommand("Ubuntu-24.04", "/home/me/go/bin/projmux")
	for _, want := range []string{
		`param([string]$uri)`,
		`'focus', '--uri', $uri`,
		`"%1"`,
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("handler command missing URI forwarding token %q:\n%s", want, command)
		}
	}
	for _, forbidden := range []string{
		`--uri "%1"`,
		`--uri '%1'`,
		`$uri = "%1"`,
		`$uri = '%1'`,
	} {
		if strings.Contains(command, forbidden) {
			t.Fatalf("handler command shell-interpolates %%1 with %q:\n%s", forbidden, command)
		}
	}
}

func TestBuildWSLURIProtocolHandlerCommand_PSEscapesDistro(t *testing.T) {
	t.Parallel()

	// Distro names with single quotes (unusual but technically allowed in
	// WSL distro registration) must be PowerShell-escaped to keep the
	// quoted string literal balanced.
	command := buildWSLURIProtocolHandlerCommand("weird's-distro", "/home/me/go/bin/projmux")
	if !strings.Contains(command, `'-d', 'weird''s-distro', '--exec'`) {
		t.Fatalf("expected single-quote-escaped distro in launch command; got:\n%s", command)
	}
}

func TestBuildWSLURIProtocolHandlerCommand_PSEscapesBinaryPath(t *testing.T) {
	t.Parallel()

	// Binary paths shouldn't contain single quotes in practice, but defend
	// the quoted PowerShell literal so a pathological install location can't
	// break the registration script.
	command := buildWSLURIProtocolHandlerCommand("Ubuntu-24.04", "/home/o'brien/bin/projmux")
	if !strings.Contains(command, `'--exec', '/home/o''brien/bin/projmux', 'focus'`) {
		t.Fatalf("expected single-quote-escaped binary path in launch command; got:\n%s", command)
	}
}
