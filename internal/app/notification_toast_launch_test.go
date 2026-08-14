package app

import (
	"strings"
	"testing"
)

// TestBuildToastPowerShell_NeverEmitsClickTarget pins the two-state delivery
// contract on the Toast payload builder: whatever the caller passes, the
// generated XML must stay a passive notification. A `launch=` /
// `activationType="protocol"` pair is what lets Windows hand a click back into
// projmux and pull the host terminal window forward, so the attribute pair
// must not be reachable from any argument shape.
func TestBuildToastPowerShell_NeverEmitsClickTarget(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		summary  string
		body     string
		tag      string
		group    string
		iconPath string
		expireMS int
	}{
		{name: "with pane tag", summary: "hello", body: "world", tag: "%8", group: "session", expireMS: defaultAINotifyExpireMS},
		{name: "without pane tag", summary: "hello", body: "world", expireMS: 2500},
		{name: "with icon and no expiry", summary: "hello", body: "world", tag: "%8", iconPath: `C:\icons\projmux.png`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			script := buildToastPowerShell(tc.summary, tc.body, desktopAppID, tc.tag, tc.group, tc.iconPath, tc.expireMS)
			for _, forbidden := range []string{
				"launch=",
				"activationType=",
				desktopURIScheme + "://",
				"pane_id=",
			} {
				if strings.Contains(script, forbidden) {
					t.Fatalf("toast script must not contain %q:\n%s", forbidden, script)
				}
			}
			if !strings.Contains(script, "CreateToastNotifier('"+desktopAppID+"').Show($toast)") {
				t.Fatalf("expected toast dispatch in script:\n%s", script)
			}
		})
	}
}

func TestBuildToastPowerShell_KeepsPassiveToastTagAndExpiry(t *testing.T) {
	t.Parallel()

	script := buildToastPowerShell("hello", "world", desktopAppID, "%8", "session", "", defaultAINotifyExpireMS)
	for _, want := range []string{
		`<toast duration="short">`,
		"$toast.Tag = '%8'",
		"$toast.Group = 'session'",
		"$toast.ExpirationTime = [DateTimeOffset]::Now.AddMilliseconds(5000)",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("toast script missing %q:\n%s", want, script)
		}
	}
}
