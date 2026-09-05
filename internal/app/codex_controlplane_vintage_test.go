package app

import (
	"reflect"
	"strings"
	"testing"
)

// TestControlPlaneVintageSeparatesAReplacedImageFromTheInstalledBuild is the
// deployment-vintage gate.
//
// `make install` replaces the file on disk and leaves every running process on
// the image it started with. This track lost two acceptance criteria to that
// twice — a lifecycle observer in one phase and the broker runtime in another,
// both still serving code from before the fix while the installed binary was
// current. A diagnosis that reads those processes and reports green certifies
// "installed, therefore deployed", and the operator's only way to catch it was
// to resolve /proc/<pid>/exe by hand.
func TestControlPlaneVintageSeparatesAReplacedImageFromTheInstalledBuild(t *testing.T) {
	const self = "/home/user/go/bin/projmux"
	images := []codexProcessImage{
		// The reader's own process, which is current by construction and is not
		// one of the processes a Codex diagnosis is taken from.
		{PID: 100, Exe: self, Cmdline: []string{self, "doctor"}},
		{PID: 200, Exe: self + procDeletedSuffix, Cmdline: []string{self, "internal", "codex-broker", "serve", "--state-domain", "/state"}},
		{PID: 300, Exe: self + procDeletedSuffix, Cmdline: []string{self, "internal", "agent-hook", "ingest", codexNativeLifecycleIngestRoute, "--pane", "%1"}},
		{PID: 301, Exe: self, Cmdline: []string{self, "internal", "agent-hook", "ingest", codexNativeLifecycleIngestRoute, "--pane", "%2"}},
		// Another projmux process that serves no control-plane role.
		{PID: 400, Exe: self, Cmdline: []string{self, "attach"}},
		// A different binary at a path this reader does not own.
		{PID: 500, Exe: "/usr/bin/tmux", Cmdline: []string{"tmux", "internal", "codex-broker", "serve"}},
	}
	vintage := projectCodexControlPlaneVintage(self, 100, images, true)
	want := codexControlPlaneVintage{Supported: true, Roles: []codexControlPlaneRoleVintage{
		{Role: codexControlPlaneRoleBroker, Processes: 1, Replaced: 1},
		{Role: codexControlPlaneRoleObserver, Processes: 2, Current: 1, Replaced: 1},
	}}
	if !reflect.DeepEqual(vintage, want) {
		t.Fatalf("vintage = %+v, want %+v", vintage, want)
	}
	if vintage.Replaced() != 2 || vintage.Observed() != 3 {
		t.Fatalf("replaced = %d, observed = %d, want 2 and 3", vintage.Replaced(), vintage.Observed())
	}
	text := codexControlPlaneVintageText(vintage)
	for _, want := range []string{codexControlPlaneRoleBroker, codexControlPlaneRoleObserver, codexProcessVintageReplaced, "older code"} {
		if !strings.Contains(text, want) {
			t.Fatalf("vintage text = %q, want it to contain %q", text, want)
		}
	}
}

// TestControlPlaneVintageStaysSilentWhenEveryImageIsCurrent pins that the
// warning is earned rather than permanent. A section that always warns is one
// an operator learns to skip.
func TestControlPlaneVintageStaysSilentWhenEveryImageIsCurrent(t *testing.T) {
	const self = "/opt/projmux"
	vintage := projectCodexControlPlaneVintage(self, 1, []codexProcessImage{
		{PID: 2, Exe: self, Cmdline: []string{self, "internal", "codex-broker", "serve"}},
	}, true)
	if vintage.Replaced() != 0 {
		t.Fatalf("replaced = %d, want 0", vintage.Replaced())
	}
	if text := codexControlPlaneVintageText(vintage); strings.Contains(text, "older code") {
		t.Fatalf("vintage text = %q, want no staleness warning when every image is current", text)
	}
}

// TestControlPlaneVintageReportsAnUnreadableProcessTableAsUnknown pins that a
// platform with no process table says so.
//
// Answering `current` there would certify exactly the falsehood this projection
// exists to prevent, on the one platform where it cannot be checked.
func TestControlPlaneVintageReportsAnUnreadableProcessTableAsUnknown(t *testing.T) {
	vintage := projectCodexControlPlaneVintage("/opt/projmux", 1, []codexProcessImage{
		{PID: 2, Exe: "/opt/projmux", Cmdline: []string{"/opt/projmux", "internal", "codex-broker", "serve"}},
	}, false)
	if vintage.Supported || len(vintage.Roles) != 0 {
		t.Fatalf("vintage = %+v, want no classification without a process table", vintage)
	}
	text := codexControlPlaneVintageText(vintage)
	if !strings.Contains(text, "unknown") {
		t.Fatalf("vintage text = %q, want it to name the answer as unknown", text)
	}
	if strings.Contains(text, codexProcessVintageCurrent) {
		t.Fatalf("vintage text = %q, want no currency claim on a platform that cannot check it", text)
	}
}

// TestControlPlaneImagePathReadsTheDeletedMarker pins the discriminator itself.
// The link target still names the installed path after a replace, so only the
// suffix separates a current image from one that no longer exists on disk.
func TestControlPlaneImagePathReadsTheDeletedMarker(t *testing.T) {
	for _, test := range []struct {
		exe          string
		wantPath     string
		wantReplaced bool
	}{
		{exe: "/home/user/go/bin/projmux", wantPath: "/home/user/go/bin/projmux"},
		{exe: "/home/user/go/bin/projmux (deleted)", wantPath: "/home/user/go/bin/projmux", wantReplaced: true},
		{exe: "  /home/user/go/bin/projmux  ", wantPath: "/home/user/go/bin/projmux"},
		{exe: "", wantPath: ""},
	} {
		path, replaced := codexProcessImagePath(test.exe)
		if path != test.wantPath || replaced != test.wantReplaced {
			t.Fatalf("codexProcessImagePath(%q) = (%q, %v), want (%q, %v)", test.exe, path, replaced, test.wantPath, test.wantReplaced)
		}
	}
}

// TestControlPlaneRoleMatchesTheInternalRouteRatherThanArgvPosition pins that
// a flag inserted ahead of the route does not drop a process out of the census
// and report the fleet as smaller than it is.
func TestControlPlaneRoleMatchesTheInternalRouteRatherThanArgvPosition(t *testing.T) {
	for _, test := range []struct {
		name    string
		cmdline []string
		want    string
	}{
		{name: "broker", cmdline: []string{"projmux", "internal", "codex-broker", "serve"}, want: codexControlPlaneRoleBroker},
		{
			name:    "broker behind a future flag",
			cmdline: []string{"projmux", "--verbose", "internal", "codex-broker", "serve", "--state-domain", "/state"},
			want:    codexControlPlaneRoleBroker,
		},
		{
			name:    "observer",
			cmdline: []string{"projmux", "internal", "agent-hook", "ingest", codexNativeLifecycleIngestRoute},
			want:    codexControlPlaneRoleObserver,
		},
		{
			name:    "a sibling hook ingest route is not an observer",
			cmdline: []string{"projmux", "internal", "agent-hook", "ingest", "claude"},
			want:    "",
		},
		{name: "no argv", cmdline: nil, want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := codexControlPlaneRole(test.cmdline); got != test.want {
				t.Fatalf("role = %q, want %q", got, test.want)
			}
		})
	}
}
