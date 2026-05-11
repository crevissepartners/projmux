package app

import (
	"strings"
	"testing"
)

func TestBuildToastPowerShell_OmitsLaunchWhenURIEmpty(t *testing.T) {
	t.Parallel()

	script := buildToastPowerShell("hello", "world", "com.crevisse.projmux", "%8", "session", "", "")
	if strings.Contains(script, "launch=") {
		t.Fatalf("expected launch attribute absent when launchURI is empty; got:\n%s", script)
	}
	if strings.Contains(script, "activationType=") {
		t.Fatalf("expected activationType attribute absent when launchURI is empty; got:\n%s", script)
	}
	if !strings.Contains(script, "<toast>") {
		t.Fatalf("expected bare <toast> open tag without launch URI; got:\n%s", script)
	}
}

func TestBuildToastPowerShell_InjectsLaunchAndProtocolActivation(t *testing.T) {
	t.Parallel()

	uri := buildFocusURI("%8", "/tmp/tmux-1000/projmux")
	script := buildToastPowerShell("hello", "world", "com.crevisse.projmux", "%8", "session", "", uri)
	if !strings.Contains(script, `activationType="protocol"`) {
		t.Fatalf("expected activationType=\"protocol\"; got:\n%s", script)
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

	script := buildRegisterURIProtocolPowerShell("projmux", "Ubuntu-24.04")
	wantSubstrings := []string{
		`$regPath = "HKCU:\SOFTWARE\Classes\projmux"`,
		`$cmdPath = "$regPath\shell\open\command"`,
		"New-Item -Path $regPath -Force",
		"New-Item -Path $cmdPath -Force",
		`Set-ItemProperty -Path $regPath -Name '(Default)' -Value 'URL:projmux'`,
		"Set-ItemProperty -Path $regPath -Name 'URL Protocol' -Value ''",
		`wsl.exe -d Ubuntu-24.04 -- projmux focus --uri "%1"`,
		`Set-ItemProperty -Path $cmdPath -Name '(Default)'`,
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(script, want) {
			t.Fatalf("register script missing %q:\n%s", want, script)
		}
	}
}

func TestBuildRegisterURIProtocolPowerShell_PSEscapesDistro(t *testing.T) {
	t.Parallel()

	// Distro names with single quotes (unusual but technically allowed in
	// WSL distro registration) must be PowerShell-escaped to keep the
	// quoted string literal balanced.
	script := buildRegisterURIProtocolPowerShell("projmux", "weird's-distro")
	if !strings.Contains(script, `wsl.exe -d weird''s-distro -- projmux focus --uri "%1"`) {
		t.Fatalf("expected single-quote-escaped distro in launch command; got:\n%s", script)
	}
}
