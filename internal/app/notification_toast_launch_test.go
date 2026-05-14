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
		`$launcherDir = Join-Path $launcherRoot 'projmux'`,
		`$launcherPath = Join-Path $launcherDir 'projmux-uri-handler.vbs'`,
		"New-Item -Path $regPath -Force",
		"New-Item -Path $cmdPath -Force",
		"New-Item -Path $launcherDir -ItemType Directory -Force",
		"Set-Content -Path $launcherPath -Value $launcherScript -Encoding ASCII",
		`Set-ItemProperty -Path $regPath -Name '(Default)' -Value 'URL:projmux'`,
		"Set-ItemProperty -Path $regPath -Name 'URL Protocol' -Value ''",
		`wscript.exe //B //Nologo "`,
		`projmux-uri-handler.vbs`,
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
		`wscript.exe //B //Nologo "`,
		`"%1"`,
		`shell.Run command, 0, False`,
		`%ComSpec% /d /s /c `,
		`CmdEscape(uri)`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing hidden launcher intent %q:\n%s", want, script)
		}
	}
	for _, forbidden := range []string{
		`powershell.exe -NoProfile -WindowStyle Hidden -ExecutionPolicy Bypass -Command`,
		`Start-Process -WindowStyle Hidden -FilePath`,
		`$psi.CreateNoWindow = $true`,
	} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("script must not register console-subsystem hidden wrapper %q:\n%s", forbidden, script)
		}
	}
}

func TestBuildWSLURIProtocolLauncherVBScript_BypassesShellWithExecAndAbsolutePath(t *testing.T) {
	t.Parallel()

	// Regression guard: the registered launch command must use `--exec`
	// (skips the user's login shell so URI query `&` separators don't get
	// parsed as background-job operators by zsh/bash) and must point at the
	// absolute WSL filesystem path to the projmux binary (because `--exec`
	// doesn't load shell init files, so PATH is unreliable). The bare
	// `-- projmux focus` form shipped in PR #178 broke on the very first
	// `&`-bearing toast click; do not let it come back.
	script := buildWSLURIProtocolLauncherVBScript("Ubuntu-24.04", "/home/me/go/bin/projmux")
	for _, want := range []string{
		`inner = "wsl.exe -d " & CmdEscape("Ubuntu-24.04") & " --exec " & CmdEscape("/home/me/go/bin/projmux") & " focus --uri " & CmdEscape(uri)`,
		`command = "%ComSpec% /d /s /c " & Chr(34) & inner & Chr(34)`,
		`shell.Run command, 0, False`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("expected launcher token %q in script:\n%s", want, script)
		}
	}
	if strings.Contains(script, `-- projmux focus`) {
		t.Fatalf("launcher must not use the legacy `-- projmux focus` form (shell-interpreted, breaks on `&`):\n%s", script)
	}
	if !strings.Contains(script, ` --exec `) {
		t.Fatalf("launcher must use `--exec` to bypass the login shell:\n%s", script)
	}
}

func TestBuildWSLURIProtocolLauncherVBScript_ForwardsURIAsArgument(t *testing.T) {
	t.Parallel()

	registerScript := buildRegisterURIProtocolPowerShell("projmux", "Ubuntu-24.04", "/home/me/go/bin/projmux")
	launcherScript := buildWSLURIProtocolLauncherVBScript("Ubuntu-24.04", "/home/me/go/bin/projmux")
	for _, want := range []string{
		`wscript.exe //B //Nologo "`,
		`"%1"`,
		`uri = WScript.Arguments.Item(0)`,
		`focus --uri " & CmdEscape(uri)`,
		`s = Replace(s, "&", "^&")`,
	} {
		if !strings.Contains(registerScript+"\n"+launcherScript, want) {
			t.Fatalf("handler command missing URI forwarding token %q:\n%s\n%s", want, registerScript, launcherScript)
		}
	}
	for _, forbidden := range []string{
		`--uri "%1"`,
		`--uri" "%1`,
		`$uri = "%1"`,
		`$uri = '%1'`,
		`param([string]$uri)`,
		`-Command`,
	} {
		if strings.Contains(registerScript, forbidden) {
			t.Fatalf("handler command shell-interpolates %%1 with %q:\n%s", forbidden, registerScript)
		}
	}
}

func TestBuildWSLURIProtocolLauncherVBScript_EscapesDistro(t *testing.T) {
	t.Parallel()

	script := buildWSLURIProtocolLauncherVBScript(`weird"distro`, "/home/me/go/bin/projmux")
	if !strings.Contains(script, `CmdEscape("weird""distro")`) {
		t.Fatalf("expected double-quote-escaped distro in launcher script; got:\n%s", script)
	}
}

func TestBuildWSLURIProtocolLauncherVBScript_EscapesBinaryPath(t *testing.T) {
	t.Parallel()

	script := buildWSLURIProtocolLauncherVBScript("Ubuntu-24.04", `/home/me/bin/proj"mux`)
	if !strings.Contains(script, `CmdEscape("/home/me/bin/proj""mux")`) {
		t.Fatalf("expected double-quote-escaped binary path in launcher script; got:\n%s", script)
	}
}

func TestVBSDoubleQuotedEscapesQuotes(t *testing.T) {
	t.Parallel()

	got := vbsDoubleQuoted(`C:\Program Files\projmux "test"\`)
	want := `"C:\Program Files\projmux ""test""\"`
	if got != want {
		t.Fatalf("vbsDoubleQuoted() = %q, want %q", got, want)
	}
}
