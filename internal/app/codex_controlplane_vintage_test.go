package app

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
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
	want := codexControlPlaneVintage{Supported: true, Roles: []projmuxProcessRoleVintage{
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

// TestProjmuxProcessVintageCountsEveryChildOfThisExecutable is the census
// completeness gate.
//
// The Codex census counts the two roles its verdicts are read from and drops
// everything else, which is correct for the line it qualifies and wrong as an
// answer to "how much of the running fleet did the last install not reach". On
// 2026-09-05 that difference was six named processes against thirty-two live
// children, with eighteen per-pane supervisors among the twenty-six the section
// never mentioned. A number that small reads as reassurance.
//
// The assertion that matters is the last one: the observed total equals the
// number of this executable's children in the table, so no route this reader
// has no name for can shrink it.
func TestProjmuxProcessVintageCountsEveryChildOfThisExecutable(t *testing.T) {
	const self = "/home/user/go/bin/projmux"
	images := []codexProcessImage{
		// The reader's own process, which is current by construction.
		{PID: 100, Exe: self, Cmdline: []string{self, "doctor"}},
		{PID: 200, Exe: self + procDeletedSuffix, Cmdline: []string{self, "internal", "codex-broker", "serve", "--state-domain", "/state"}},
		{PID: 300, Exe: self + procDeletedSuffix, Cmdline: []string{self, "internal", "agent-hook", "ingest", codexNativeLifecycleIngestRoute, "--pane", "%1"}},
		{PID: 301, Exe: self, Cmdline: []string{self, "internal", "agent-hook", "ingest", codexNativeLifecycleIngestRoute, "--pane", "%2"}},
		{PID: 400, Exe: self + procDeletedSuffix, Cmdline: []string{self, "internal", "supervise", "--pane-uid", "pane-a"}},
		{PID: 401, Exe: self + procDeletedSuffix, Cmdline: []string{self, "internal", "supervise", "--pane-uid", "pane-b"}},
		{PID: 402, Exe: self, Cmdline: []string{self, "internal", "supervise", "--pane-uid", "pane-c"}},
		// Routes this census has no name for. They are still projmux processes
		// running a projmux image, so they stay counted.
		{PID: 500, Exe: self + procDeletedSuffix, Cmdline: []string{self, "internal", "claude-endpoint-helper"}},
		{PID: 501, Exe: self, Cmdline: []string{self, "attach"}},
		// An argv this reader could not read at all.
		{PID: 502, Exe: self, Cmdline: nil},
		// A different binary at a path this reader does not own.
		{PID: 600, Exe: "/usr/bin/tmux", Cmdline: []string{"tmux", "internal", "supervise"}},
	}
	fleet := projectProjmuxProcessVintage(self, 100, images, true)
	want := projmuxProcessVintage{Supported: true, Roles: []projmuxProcessRoleVintage{
		{Role: codexControlPlaneRoleBroker, Processes: 1, Replaced: 1},
		{Role: codexControlPlaneRoleObserver, Processes: 2, Current: 1, Replaced: 1},
		{Role: projmuxProcessRoleSupervisor, Processes: 3, Current: 1, Replaced: 2},
		{Role: projmuxProcessRoleOther, Processes: 3, Current: 2, Replaced: 1},
	}}
	if !reflect.DeepEqual(fleet, want) {
		t.Fatalf("fleet vintage = %+v, want %+v", fleet, want)
	}
	children := 0
	for _, image := range images {
		if path, _ := codexProcessImagePath(image.Exe); path == self && image.PID != 100 {
			children++
		}
	}
	if fleet.Observed() != children {
		t.Fatalf("observed = %d, want every one of the %d children of this executable", fleet.Observed(), children)
	}
	if fleet.Replaced() != 5 {
		t.Fatalf("replaced = %d, want 5", fleet.Replaced())
	}
	text := projmuxProcessVintageText(fleet)
	for _, want := range []string{projmuxProcessRoleSupervisor, projmuxProcessRoleOther, "5 of 9"} {
		if !strings.Contains(text, want) {
			t.Fatalf("fleet vintage text = %q, want it to contain %q", text, want)
		}
	}
}

// TestCodexControlPlaneVintageIgnoresProviderNeutralProcesses pins the other
// half of the split. The fleet census gained supervisors and a remainder
// bucket; the Codex line must not, because it qualifies verdicts read from two
// specific processes and a supervisor is not one of them. It supervises a
// Claude pane exactly as it supervises a Codex one.
func TestCodexControlPlaneVintageIgnoresProviderNeutralProcesses(t *testing.T) {
	const self = "/opt/projmux"
	images := []codexProcessImage{
		{PID: 200, Exe: self, Cmdline: []string{self, "internal", "codex-broker", "serve"}},
		{PID: 400, Exe: self + procDeletedSuffix, Cmdline: []string{self, "internal", "supervise", "--pane-uid", "pane-a"}},
		{PID: 500, Exe: self + procDeletedSuffix, Cmdline: []string{self, "internal", "claude-endpoint-helper"}},
	}
	vintage := projectCodexControlPlaneVintage(self, 1, images, true)
	want := codexControlPlaneVintage{Supported: true, Roles: []projmuxProcessRoleVintage{
		{Role: codexControlPlaneRoleBroker, Processes: 1, Current: 1},
	}}
	if !reflect.DeepEqual(vintage, want) {
		t.Fatalf("codex vintage = %+v, want only the roles this section reads its verdicts from", vintage)
	}
	text := codexControlPlaneVintageText(vintage)
	for _, unwanted := range []string{projmuxProcessRoleSupervisor, projmuxProcessRoleOther} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("codex vintage text = %q, want no provider-neutral role named as a Codex control-plane role", text)
		}
	}
	if strings.Contains(text, "older code") {
		t.Fatalf("codex vintage text = %q, want a replaced supervisor not to warn about the Codex verdicts below it", text)
	}
}

// TestProjmuxProcessRoleNamesEveryChildIncludingTheOnesItCannotName is the
// classification table.
//
// The default arm is the point. Before it, an argv that matched no named route
// resolved to "" and then failed a map lookup, and the process left the census
// without leaving a trace of having been there.
func TestProjmuxProcessRoleNamesEveryChildIncludingTheOnesItCannotName(t *testing.T) {
	for _, test := range []struct {
		name    string
		cmdline []string
		want    string
		codex   string
	}{
		{
			name:    "broker",
			cmdline: []string{"projmux", "internal", "codex-broker", "serve"},
			want:    codexControlPlaneRoleBroker,
			codex:   codexControlPlaneRoleBroker,
		},
		{
			name:    "observer",
			cmdline: []string{"projmux", "internal", "agent-hook", "ingest", codexNativeLifecycleIngestRoute},
			want:    codexControlPlaneRoleObserver,
			codex:   codexControlPlaneRoleObserver,
		},
		{
			name:    "supervisor",
			cmdline: []string{"projmux", "internal", "supervise", "--pane-uid", "pane-a"},
			want:    projmuxProcessRoleSupervisor,
		},
		{
			name:    "supervisor behind a future flag",
			cmdline: []string{"projmux", "--verbose", "internal", "supervise", "--pane-uid", "pane-a"},
			want:    projmuxProcessRoleSupervisor,
		},
		{
			name:    "a route this census has no name for",
			cmdline: []string{"projmux", "internal", "claude-endpoint-helper"},
			want:    projmuxProcessRoleOther,
		},
		{
			name:    "a sibling hook ingest route",
			cmdline: []string{"projmux", "internal", "agent-hook", "ingest", "claude"},
			want:    projmuxProcessRoleOther,
		},
		{name: "an argv this reader could not read", cmdline: nil, want: projmuxProcessRoleOther},
		{name: "an argv that is only the executable", cmdline: []string{"projmux"}, want: projmuxProcessRoleOther},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := projmuxProcessRole(test.cmdline); got != test.want {
				t.Fatalf("role = %q, want %q", got, test.want)
			}
			if got := codexControlPlaneRole(test.cmdline); got != test.codex {
				t.Fatalf("codex role = %q, want %q", got, test.codex)
			}
		})
	}
}

// TestProjmuxProcessRouteWordsStopAtTheArgvSeparator keeps a message about a
// process from being counted as one.
//
// `projmux agent turn ... -- <text>` puts arbitrary prose in argv, and this
// track's own delegation prompts name `codex-broker serve` and `supervise` in
// that text. Matching route words anywhere in argv would count a message about
// the broker as a second broker and report a fleet larger than it is, which is
// the same class of error as reporting one smaller.
func TestProjmuxProcessRouteWordsStopAtTheArgvSeparator(t *testing.T) {
	message := []string{
		"projmux", "agent", "turn", "steer", "uid:agent-1", "--",
		"do", "not", "kill", "the", "codex-broker", "serve", "process", "or", "any", "supervise", "child",
	}
	if got := projmuxProcessRole(message); got != projmuxProcessRoleOther {
		t.Fatalf("role = %q, want the words behind `--` to be read as a message rather than a route", got)
	}
	if got := codexControlPlaneRole(message); got != "" {
		t.Fatalf("codex role = %q, want no control-plane role for a message that merely names one", got)
	}
	route := []string{"projmux", "internal", "supervise", "--pane-uid", "pane-a"}
	if got := projmuxProcessRouteWords(route); !reflect.DeepEqual(got, []string{"internal", "supervise", "--pane-uid", "pane-a"}) {
		t.Fatalf("route words = %q, want the argv without its executable path", got)
	}
	if got := projmuxProcessRouteWords([]string{"/opt/serve/projmux", "attach"}); slices.Contains(got, "/opt/serve/projmux") {
		t.Fatalf("route words = %q, want argv[0] excluded so an executable path cannot spell a route", got)
	}
}

// TestProjmuxProcessVintageReportsAnUnreadableProcessTableAsUnknown pins that
// the fleet census answers `unknown` where the Codex one does.
//
// Darwin exposes no /proc/<pid>/exe, so a replaced image cannot be told apart
// from the installed one there. Answering `current` would certify exactly the
// falsehood this projection exists to prevent, on the one platform where it
// cannot be checked.
func TestProjmuxProcessVintageReportsAnUnreadableProcessTableAsUnknown(t *testing.T) {
	fleet := projectProjmuxProcessVintage("/opt/projmux", 1, []codexProcessImage{
		{PID: 2, Exe: "/opt/projmux", Cmdline: []string{"/opt/projmux", "internal", "supervise"}},
	}, false)
	if fleet.Supported || len(fleet.Roles) != 0 {
		t.Fatalf("fleet vintage = %+v, want no classification without a process table", fleet)
	}
	if fleet.Observed() != 0 || fleet.Replaced() != 0 {
		t.Fatalf("observed = %d, replaced = %d, want no counts without a process table", fleet.Observed(), fleet.Replaced())
	}
	text := projmuxProcessVintageText(fleet)
	if !strings.Contains(text, "unknown") {
		t.Fatalf("fleet vintage text = %q, want it to name the answer as unknown", text)
	}
	if strings.Contains(text, codexProcessVintageCurrent) {
		t.Fatalf("fleet vintage text = %q, want no currency claim on a platform that cannot check it", text)
	}
}

// TestProcessVintageNamesNoProcess is the negative audit.
//
// A census is printable on a diagnostics surface only because it is counts and
// nothing else. Adding roles widened what the reader classifies; it must not
// widen what the reader says.
func TestProcessVintageNamesNoProcess(t *testing.T) {
	const self = "/home/operator/go/bin/projmux"
	images := []codexProcessImage{
		{PID: 987654, Exe: self + procDeletedSuffix, Cmdline: []string{self, "internal", "supervise", "--pane-uid", "pane-secret"}},
		{PID: 987655, Exe: self, Cmdline: []string{self, "internal", "codex-broker", "serve", "--state-domain", "/home/operator/state"}},
		{PID: 987656, Exe: self, Cmdline: []string{self, "internal", "claude-endpoint-helper", "--endpoint", "/home/operator/socket"}},
	}
	rendered := []string{
		projmuxProcessVintageText(projectProjmuxProcessVintage(self, 1, images, true)),
		codexControlPlaneVintageText(projectCodexControlPlaneVintage(self, 1, images, true)),
	}
	for _, text := range rendered {
		for _, leak := range []string{
			self, "987654", "987655", "987656",
			"pane-secret", "--pane-uid", "--state-domain", "--endpoint",
			"/home/operator", "claude-endpoint-helper", "internal",
		} {
			if strings.Contains(text, leak) {
				t.Fatalf("vintage text = %q, want no pid, path or argv on a diagnostics surface; found %q", text, leak)
			}
		}
	}
}

// TestDoctorSeparatesTheCodexDiagnosisVintageFromTheFleetCensus pins that the
// two questions read as two questions.
//
// "Which image was this Codex diagnosis read from" qualifies the verdicts
// printed under it. "How many projmux processes are still running the image
// from before the last install" is a fleet question with no provider in it.
// Merging them would have put a provider-neutral supervisor on a Codex line and
// answered neither.
func TestDoctorSeparatesTheCodexDiagnosisVintageFromTheFleetCensus(t *testing.T) {
	fleet := projmuxProcessVintage{Supported: true, Roles: []projmuxProcessRoleVintage{
		{Role: codexControlPlaneRoleBroker, Processes: 1, Replaced: 1},
		{Role: projmuxProcessRoleSupervisor, Processes: 18, Replaced: 18},
		{Role: projmuxProcessRoleOther, Processes: 8, Current: 8},
	}}
	report := doctorReport{
		SchemaVersion: doctorSchemaVersion,
		CodexControlPlane: &codexControlPlaneReport{Vintage: codexControlPlaneVintage{
			Supported: true,
			Roles: []projmuxProcessRoleVintage{
				{Role: codexControlPlaneRoleBroker, Processes: 1, Replaced: 1},
			},
		}},
		ProcessVintage: &fleet,
	}
	var buf bytes.Buffer
	if err := writeDoctorText(&buf, report, doctorSectionAll, false); err != nil {
		t.Fatalf("writeDoctorText: %v", err)
	}
	text := buf.String()
	codexLine, fleetLine := "", ""
	for line := range strings.SplitSeq(text, "\n") {
		switch {
		case strings.Contains(line, "Diagnosis vintage:"):
			codexLine = line
		case strings.Contains(line, "Children of this executable:"):
			fleetLine = line
		}
	}
	if codexLine == "" || fleetLine == "" {
		t.Fatalf("doctor text = %q, want both a Codex diagnosis vintage line and a fleet census line", text)
	}
	if strings.Contains(codexLine, projmuxProcessRoleSupervisor) {
		t.Fatalf("codex line = %q, want no provider-neutral role on the line that qualifies Codex verdicts", codexLine)
	}
	if !strings.Contains(fleetLine, projmuxProcessRoleSupervisor) || !strings.Contains(fleetLine, "18") {
		t.Fatalf("fleet line = %q, want the per-pane supervisors counted", fleetLine)
	}
	if !strings.Contains(text, "\nProjmux process vintage\n") {
		t.Fatalf("doctor text = %q, want the fleet census under its own heading", text)
	}
	fleetHeading := strings.Index(text, "\nProjmux process vintage\n")
	codexHeading := strings.Index(text, "\nCodex control-plane surfaces\n")
	if fleetHeading < 0 || codexHeading < 0 || fleetHeading > codexHeading {
		t.Fatalf("doctor text = %q, want the fleet census as its own block outside the Codex section", text)
	}
}

// TestProjmuxProcessVintageMeasuresTheResidualAgeDistribution is the age half
// of the install residue measurement.
//
// A bounded drain of the processes an install left behind can only be designed
// against how long those processes have already been alive, and the shape of
// that answer has to be the distribution: a mean over twenty supervisors says
// nothing about the one that outlives every plausible bound. Ages are collected
// only for the replaced processes -- a current-image process is not residue and
// has nothing to drain.
func TestProjmuxProcessVintageMeasuresTheResidualAgeDistribution(t *testing.T) {
	const self = "/home/user/go/bin/projmux"
	now := time.Date(2026, 9, 6, 4, 12, 33, 0, time.UTC)
	images := []codexProcessImage{
		{PID: 100, Exe: self, Cmdline: []string{self, "doctor"}, StartedAt: now.Add(-time.Second)},
		{
			PID: 200, Exe: self + procDeletedSuffix,
			Cmdline:   []string{self, "internal", "codex-broker", "serve"},
			StartedAt: now.Add(-2*time.Hour - 14*time.Minute),
		},
		{
			PID: 400, Exe: self + procDeletedSuffix,
			Cmdline:   []string{self, "internal", "supervise", "--pane-uid", "pane-a"},
			StartedAt: now.Add(-3*time.Hour - 2*time.Minute),
		},
		{
			PID: 401, Exe: self + procDeletedSuffix,
			Cmdline:   []string{self, "internal", "supervise", "--pane-uid", "pane-b"},
			StartedAt: now.Add(-41 * time.Second),
		},
		// A current-image supervisor. It is not residue, so it contributes no
		// age even though its start time is perfectly readable.
		{
			PID: 402, Exe: self,
			Cmdline:   []string{self, "internal", "supervise", "--pane-uid", "pane-c"},
			StartedAt: now.Add(-9 * time.Hour),
		},
		// A clock that moved backwards between the two reads. Zero is the
		// honest floor; a negative age would read as a process started after
		// the census that observed it.
		{
			PID: 403, Exe: self + procDeletedSuffix,
			Cmdline:   []string{self, "internal", "supervise", "--pane-uid", "pane-d"},
			StartedAt: now.Add(5 * time.Second),
		},
	}
	fleet := projectProjmuxProcessVintageAt(self, 100, images, true, now)
	want := projmuxProcessVintage{Supported: true, Roles: []projmuxProcessRoleVintage{
		{Role: codexControlPlaneRoleBroker, Processes: 1, Replaced: 1, ReplacedAgeSeconds: []int{8040}},
		{
			Role: projmuxProcessRoleSupervisor, Processes: 4, Current: 1, Replaced: 3,
			ReplacedAgeSeconds: []int{0, 41, 10920},
		},
	}}
	if !reflect.DeepEqual(fleet, want) {
		t.Fatalf("fleet vintage = %+v, want %+v", fleet, want)
	}
}

// TestProjmuxProcessVintageKeepsAnUnknownStartTimeOutOfTheDistribution pins
// that a process whose start time could not be read still counts as residue and
// still contributes no age.
//
// Both halves matter. Dropping the process would report the install as having
// left less behind than it did; inventing an age for it would put a number into
// the distribution a drain bound is read from that no observation supports.
func TestProjmuxProcessVintageKeepsAnUnknownStartTimeOutOfTheDistribution(t *testing.T) {
	const self = "/opt/projmux"
	now := time.Date(2026, 9, 6, 4, 12, 33, 0, time.UTC)
	fleet := projectProjmuxProcessVintageAt(self, 1, []codexProcessImage{
		{PID: 2, Exe: self + procDeletedSuffix, Cmdline: []string{self, "internal", "supervise"}},
		{
			PID: 3, Exe: self + procDeletedSuffix,
			Cmdline:   []string{self, "internal", "supervise"},
			StartedAt: now.Add(-10 * time.Minute),
		},
	}, true, now)
	want := []projmuxProcessRoleVintage{
		{Role: projmuxProcessRoleSupervisor, Processes: 2, Replaced: 2, ReplacedAgeSeconds: []int{600}},
	}
	if !reflect.DeepEqual(fleet.Roles, want) {
		t.Fatalf("roles = %+v, want %+v", fleet.Roles, want)
	}
	if fleet.Replaced() != 2 {
		t.Fatalf("replaced = %d, want both residual processes counted", fleet.Replaced())
	}
}

// TestProjmuxProcessVintageCapsOneRoleAgeDistribution keeps a pathological
// process table from making one record unbounded, and makes the census say that
// the distribution it carries is a prefix rather than the whole of it.
func TestProjmuxProcessVintageCapsOneRoleAgeDistribution(t *testing.T) {
	const self = "/opt/projmux"
	now := time.Date(2026, 9, 6, 4, 12, 33, 0, time.UTC)
	images := []codexProcessImage{{PID: 1, Exe: self, Cmdline: []string{self, "doctor"}}}
	for index := range projmuxProcessRoleAgeSampleLimit + 7 {
		images = append(images, codexProcessImage{
			PID: 1000 + index, Exe: self + procDeletedSuffix,
			Cmdline:   []string{self, "internal", "supervise"},
			StartedAt: now.Add(-time.Duration(index+1) * time.Second),
		})
	}
	fleet := projectProjmuxProcessVintageAt(self, 1, images, true, now)
	if len(fleet.Roles) != 1 {
		t.Fatalf("roles = %+v, want one supervisor role", fleet.Roles)
	}
	role := fleet.Roles[0]
	if role.Replaced != projmuxProcessRoleAgeSampleLimit+7 {
		t.Fatalf("replaced = %d, want every residual process still counted", role.Replaced)
	}
	if len(role.ReplacedAgeSeconds) != projmuxProcessRoleAgeSampleLimit {
		t.Fatalf("ages = %d, want the sample bound of %d", len(role.ReplacedAgeSeconds), projmuxProcessRoleAgeSampleLimit)
	}
	if !role.ReplacedAgeCapped {
		t.Fatal("the census hit its sample bound and did not say so, which reports a prefix as a whole distribution")
	}
}

// TestDoctorProcessVintageProjectionCarriesNoAges pins that the diagnosis
// projection is untouched by the residual measurement.
//
// `projmux doctor` renders counts and its record is read by other surfaces. The
// age fields are populated only when a caller supplies a reference instant, so
// the doctor projection stays exactly the record it was.
func TestDoctorProcessVintageProjectionCarriesNoAges(t *testing.T) {
	const self = "/opt/projmux"
	now := time.Date(2026, 9, 6, 4, 12, 33, 0, time.UTC)
	images := []codexProcessImage{
		{PID: 1, Exe: self, Cmdline: []string{self, "doctor"}},
		{
			PID: 2, Exe: self + procDeletedSuffix,
			Cmdline:   []string{self, "internal", "supervise"},
			StartedAt: now.Add(-time.Hour),
		},
	}
	fleet := projectProjmuxProcessVintage(self, 1, images, true)
	want := []projmuxProcessRoleVintage{{Role: projmuxProcessRoleSupervisor, Processes: 1, Replaced: 1}}
	if !reflect.DeepEqual(fleet.Roles, want) {
		t.Fatalf("doctor roles = %+v, want %+v", fleet.Roles, want)
	}
	body, err := json.Marshal(fleet)
	if err != nil {
		t.Fatalf("marshal fleet vintage: %v", err)
	}
	for _, absent := range []string{"replacedAgeSeconds", "replacedAgeCapped"} {
		if strings.Contains(string(body), absent) {
			t.Fatalf("doctor vintage JSON = %s, want %q absent", body, absent)
		}
	}
}
