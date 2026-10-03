package app

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/i18n"
)

func TestInstallReplacementTargetsOnlyNameRemainingDrainableImages(t *testing.T) {
	t.Parallel()
	const self = "/test/bin/projmux"
	broker := []string{self, "internal", "codex-broker", "serve"}
	images := []codexProcessImage{
		{PID: 8, Exe: self + procDeletedSuffix, Cmdline: broker},
		{PID: 7, Exe: self + procDeletedSuffix, Cmdline: broker},
		{PID: 6, Exe: self, Cmdline: broker}, // already on the installed image
		{PID: 5, Exe: "/other/projmux" + procDeletedSuffix, Cmdline: broker},
		{PID: 4, Exe: self + procDeletedSuffix, Cmdline: []string{self, "internal", "agent-hook", "ingest", "codex-broker-watch"}},
		{PID: 3, Exe: self + procDeletedSuffix, Cmdline: []string{self, "shell"}},
		{PID: 2, Exe: self + procDeletedSuffix, Cmdline: broker}, // the reader
		{PID: 0, Exe: self + procDeletedSuffix, Cmdline: broker},
	}
	var revisionReads []int
	got := projectInstallReplacementTargets(self, 2, images, func(image codexProcessImage) string {
		revisionReads = append(revisionReads, image.PID)
		if image.PID == 7 {
			return "8ae6e563"
		}
		return ""
	}, nil)
	want := []installReplacementTarget{
		{role: codexControlPlaneRoleBroker, pid: 7, revision: "8ae6e563"},
		{role: codexControlPlaneRoleBroker, pid: 8, revision: "unknown"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("targets = %+v, want %+v", got, want)
	}
	if !reflect.DeepEqual(revisionReads, []int{8, 7}) {
		t.Fatalf("read revisions for non-targets: %v", revisionReads)
	}
	if got := projectInstallReplacementTargets("", 2, images, nil, nil); len(got) != 0 {
		t.Fatalf("unknown executable named targets: %+v", got)
	}
	if got := installReplacementProcessRevision(codexProcessImage{PID: -1}); got != "" {
		t.Fatalf("unreadable process revision = %q, want unknown", got)
	}
}

// TestInstallReplacementTargetNamesAnotherDomainsBroker is the case that
// misled an operator: a broker an isolated probe started from the same binary
// under another HOME. Its line names the other domain and both of its paths.
func TestInstallReplacementTargetNamesAnotherDomainsBroker(t *testing.T) {
	t.Parallel()
	const self = "/test/bin/projmux"
	images := []codexProcessImage{{
		PID: 9, Exe: self + procDeletedSuffix,
		Cmdline: []string{self, "internal", "codex-broker", "serve", "--state-domain", "/tmp/probe/.local/state/projmux"},
	}}
	environ := func(codexProcessImage) (installReplacementEnviron, bool) {
		return installReplacementEnviron{home: "/tmp/probe"}, true
	}
	targets := projectInstallReplacementTargets(self, 2, images, nil,
		installReplacementTargetOriginReader("/home/operator/.local/state/projmux", environ))
	want := []installReplacementTarget{{
		role: codexControlPlaneRoleBroker, pid: 9, revision: "unknown",
		origin: installReplacementTargetOrigin{
			domain: installReplacementDomainOther, stateDomain: "/tmp/probe/.local/state/projmux", home: "/tmp/probe",
		},
	}}
	if !reflect.DeepEqual(targets, want) {
		t.Fatalf("targets = %+v, want %+v", targets, want)
	}
	text := renderInstallReplacementFailure(targets, i18n.FallbackLocale)
	line := "role=broker-runtime pid=9 revision=unknown domain=other stateDomain=/tmp/probe/.local/state/projmux home=/tmp/probe\n"
	if !strings.Contains(text, line) {
		t.Fatalf("failure omitted %q: %s", line, text)
	}
}

// TestInstallReplacementTargetOriginResolvesThisDomain covers both ways a
// process names its domain: the managed broker's explicit flag, and HOME with
// XDG_STATE_HOME for a process launched without one.
func TestInstallReplacementTargetOriginResolvesThisDomain(t *testing.T) {
	t.Parallel()
	const executor = "/home/operator/.local/state/projmux"
	broker := []string{"/bin/projmux", "internal", "codex-broker", "serve"}
	for _, tc := range []struct {
		name    string
		cmdline []string
		env     installReplacementEnviron
		want    installReplacementTargetOrigin
	}{
		{
			name:    "argv flag",
			cmdline: append(slices.Clone(broker), "--state-domain", executor+"/"),
			env:     installReplacementEnviron{home: "/home/operator"},
			want:    installReplacementTargetOrigin{domain: installReplacementDomainThis, stateDomain: executor, home: "/home/operator"},
		},
		{
			name:    "inline argv flag wins over the environment",
			cmdline: append(slices.Clone(broker), "--state-domain="+executor),
			env:     installReplacementEnviron{home: "/tmp/elsewhere"},
			want:    installReplacementTargetOrigin{domain: installReplacementDomainThis, stateDomain: executor, home: "/tmp/elsewhere"},
		},
		{
			name:    "home fallback",
			cmdline: broker,
			env:     installReplacementEnviron{home: "/home/operator"},
			want:    installReplacementTargetOrigin{domain: installReplacementDomainThis, stateDomain: executor, home: "/home/operator"},
		},
		{
			name:    "XDG_STATE_HOME fallback",
			cmdline: broker,
			env:     installReplacementEnviron{home: "/tmp/probe", stateHome: "/home/operator/.local/state"},
			want:    installReplacementTargetOrigin{domain: installReplacementDomainThis, stateDomain: executor, home: "/tmp/probe"},
		},
		{
			name:    "relative XDG_STATE_HOME counts as unset",
			cmdline: broker,
			env:     installReplacementEnviron{home: "/tmp/probe", stateHome: "state"},
			want:    installReplacementTargetOrigin{domain: installReplacementDomainOther, stateDomain: "/tmp/probe/.local/state/projmux", home: "/tmp/probe"},
		},
		{
			name: "the flag is not read behind a bare --, nor from a longer flag name",
			cmdline: append(slices.Clone(broker), "--endpoint-state-domain", "/tmp/other/.local/state/projmux",
				"--", "--state-domain", "/tmp/other/.local/state/projmux"),
			env:  installReplacementEnviron{home: "/home/operator"},
			want: installReplacementTargetOrigin{domain: installReplacementDomainThis, stateDomain: executor, home: "/home/operator"},
		},
	} {
		got := resolveInstallReplacementTargetOrigin(executor, tc.cmdline, tc.env, true)
		if got != tc.want {
			t.Errorf("%s: origin = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// TestInstallReplacementTargetOriginUnknownKeepsTheRestOfTheLine holds the
// failure case: an unreadable environment, an unresolved executor domain, or a
// vanished process make the verdict unknown and change nothing else, the exit
// code included.
func TestInstallReplacementTargetOriginUnknownKeepsTheRestOfTheLine(t *testing.T) {
	t.Parallel()
	broker := []string{"/bin/projmux", "internal", "codex-broker", "serve"}
	unknown := installReplacementTargetOrigin{domain: installReplacementDomainUnknown}
	if got := resolveInstallReplacementTargetOrigin("/home/operator/.local/state/projmux", broker, installReplacementEnviron{}, false); got != unknown {
		t.Fatalf("unreadable environment origin = %+v, want %+v", got, unknown)
	}
	flagged := append(slices.Clone(broker), "--state-domain", "/tmp/probe/.local/state/projmux")
	if got := resolveInstallReplacementTargetOrigin("", flagged, installReplacementEnviron{}, false); got.domain != installReplacementDomainUnknown || got.home != "" {
		t.Fatalf("unresolved executor origin = %+v, want unknown verdict and home", got)
	}
	if got := resolveInstallReplacementTargetOrigin("/home/operator/.local/state/projmux", flagged, installReplacementEnviron{}, false); got.domain != installReplacementDomainOther || got.home != "" {
		t.Fatalf("argv domain without environment = %+v, want other with unknown home", got)
	}
	if _, ok := readInstallReplacementEnviron(codexProcessImage{PID: -1}); ok {
		t.Fatal("a nonexistent process yielded an environment")
	}

	var stderr bytes.Buffer
	runTestInstallReplacement(t, installReplacementFixture{
		stderr: &stderr,
		vintage: projmuxProcessVintage{Supported: true, Roles: []projmuxProcessRoleVintage{
			{Role: codexControlPlaneRoleBroker, Processes: 1, Replaced: 1},
		}},
		targets: []installReplacementTarget{{role: codexControlPlaneRoleBroker, pid: 4321, revision: "8ae6e563", origin: unknown}},
		refusal: "host-unavailable",
	})
	line := "     role=broker-runtime pid=4321 revision=8ae6e563 domain=unknown stateDomain=unknown home=unknown\n"
	if !strings.Contains(stderr.String(), line) {
		t.Fatalf("failure omitted %q: %s", line, stderr.String())
	}
}

// TestInstallReplacementTargetEnvironKeepsOnlyHomeAndStateHome reads a real
// child's environment that carries a credential-shaped value, and holds that
// neither the terminal line nor the persisted record carries it.
func TestInstallReplacementTargetEnvironKeepsOnlyHomeAndStateHome(t *testing.T) {
	t.Parallel()
	const secret = "environ-sentinel-must-not-leak"
	block := "PATH=/usr/bin\x00GH_TOKEN=" + secret + "\x00HOME=/tmp/probe\x00XDG_STATE_HOME=/tmp/probe/state\x00LANG=C"
	env, err := scanInstallReplacementEnviron(strings.NewReader(block))
	if err != nil {
		t.Fatal(err)
	}
	if want := (installReplacementEnviron{home: "/tmp/probe", stateHome: "/tmp/probe/state"}); env != want {
		t.Fatalf("environ = %+v, want %+v", env, want)
	}

	if _, err := os.Stat("/proc/self/environ"); err != nil {
		t.Skip("no procfs on this platform")
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep is unavailable")
	}
	home := t.TempDir()
	child := exec.Command(sleep, "30")
	child.Env = []string{"GH_TOKEN=" + secret, "HOME=" + home, "PATH=/usr/bin:/bin"}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	pid := strconv.Itoa(child.Process.Pid)
	// Right after exec the kernel may still show an empty environment, so
	// wait until the new image's block is in place.
	var image codexProcessImage
	deadline := time.Now().Add(5 * time.Second)
	for {
		exe, err := os.Readlink("/proc/" + pid + "/exe")
		if err == nil {
			image = codexProcessImage{PID: child.Process.Pid, Exe: exe, Cmdline: []string{sleep, "30"}}
			if env, ok := readInstallReplacementEnviron(image); ok && env.home != "" {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("child environment never became readable: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := readInstallReplacementEnviron(codexProcessImage{PID: image.PID, Exe: "/recycled/pid"}); ok {
		t.Fatal("a changed executable link lent its environment to the target")
	}
	origin := installReplacementTargetOriginReader("/home/operator/.local/state/projmux", readInstallReplacementEnviron)(image)
	if origin.home != home || origin.domain != installReplacementDomainOther {
		t.Fatalf("live origin = %+v, want home %s in another domain", origin, home)
	}

	dir := t.TempDir()
	var stderr bytes.Buffer
	runTestInstallReplacement(t, installReplacementFixture{
		stateDir: dir,
		stderr:   &stderr,
		vintage: projmuxProcessVintage{Supported: true, Roles: []projmuxProcessRoleVintage{
			{Role: codexControlPlaneRoleBroker, Processes: 1, Replaced: 1},
		}},
		targets: []installReplacementTarget{{role: codexControlPlaneRoleBroker, pid: image.PID, revision: "unknown", origin: origin}},
		refusal: "host-unavailable",
	})
	if !strings.Contains(stderr.String(), "home="+home) {
		t.Fatalf("failure omitted the target home: %s", stderr.String())
	}
	body, err := os.ReadFile(filepath.Join(dir, installReplacementFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{stderr.String(), string(body)} {
		if strings.Contains(text, secret) || strings.Contains(text, "GH_TOKEN") {
			t.Fatalf("environment value leaked: %s", text)
		}
	}
	for _, private := range []string{home, "stateDomain", "domain=", pid} {
		if strings.Contains(string(body), private) {
			t.Fatalf("persisted record carries %q: %s", private, body)
		}
	}
}

func TestInstallReplacementTargetValueQuotesUnsafePaths(t *testing.T) {
	t.Parallel()
	for value, want := range map[string]string{
		"":                 "unknown",
		"/tmp/probe":       "/tmp/probe",
		"/tmp/a b":         `"/tmp/a b"`,
		"/tmp/\x1b[2Jx":    `"/tmp/\x1b[2Jx"`,
		"/tmp/x\ndomain=a": `"/tmp/x\ndomain=a"`,
	} {
		if got := renderInstallReplacementTargetValue(value); got != want {
			t.Errorf("value %q rendered %q, want %q", value, got, want)
		}
	}
}

func TestInstallReplacementOriginRequiresStableProcessIdentity(t *testing.T) {
	t.Parallel()
	one, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	two, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	image := codexProcessImage{PID: 9, Exe: "/bin/projmux (deleted)", Cmdline: []string{"projmux", "internal", "codex-broker", "serve", "--state-domain", "/other"}}
	stable := installReplacementIdentity{directory: one, birth: "1234", exe: image.Exe, cmdline: image.Cmdline}
	for _, tc := range []struct {
		name       string
		mutate     func(*installReplacementIdentity)
		unreadable bool
		want       string
	}{
		{name: "stable explicit argv despite unreadable environment", want: installReplacementDomainOther},
		{name: "recycled pid", mutate: func(v *installReplacementIdentity) { v.birth = "1235" }, want: installReplacementDomainUnknown},
		{name: "replaced directory", mutate: func(v *installReplacementIdentity) { v.directory = two }, want: installReplacementDomainUnknown},
		{name: "changed image", mutate: func(v *installReplacementIdentity) { v.exe = "/other/projmux" }, want: installReplacementDomainUnknown},
		{name: "changed argv", mutate: func(v *installReplacementIdentity) { v.cmdline = []string{"other"} }, want: installReplacementDomainUnknown},
		{name: "unreadable identity", unreadable: true, want: installReplacementDomainUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			reader := checkedInstallReplacementTargetOriginReader("/this", func(codexProcessImage) (installReplacementEnviron, bool) { return installReplacementEnviron{}, false }, func(codexProcessImage) (installReplacementIdentity, bool) {
				calls++
				v := stable
				if calls == 2 && tc.mutate != nil {
					tc.mutate(&v)
				}
				return v, !tc.unreadable
			})
			got := reader(image)
			if got.domain != tc.want {
				t.Fatalf("origin=%+v", got)
			}
			if tc.want == installReplacementDomainUnknown && (got.stateDomain != "" || got.home != "") {
				t.Fatal("uncertain process lent paths to verdict")
			}
		})
	}
}

func TestInstallReplacementIdentityChecksActualImageAndArgv(t *testing.T) {
	t.Parallel()
	root := "/proc/" + strconv.Itoa(os.Getpid())
	info, err := os.Stat(root)
	if err != nil {
		t.Skip("procfs unavailable")
	}
	exe, err := os.Readlink(root + "/exe")
	if err != nil {
		t.Fatal(err)
	}
	argv, err := os.ReadFile(root + "/cmdline")
	if err != nil {
		t.Fatal(err)
	}
	image := codexProcessImage{PID: os.Getpid(), Exe: exe, StartedAt: info.ModTime(), Cmdline: strings.Split(strings.TrimRight(string(argv), "\x00"), "\x00")}
	got, ok := readInstallReplacementIdentity(image)
	if !ok || got.birth == "" {
		t.Fatal("positive process identity unreadable")
	}
	image.Exe += " (deleted)"
	if _, ok := readInstallReplacementIdentity(image); ok {
		t.Fatal("changed executable accepted")
	}
	image.Exe = exe
	image.Cmdline = []string{"invented"}
	if _, ok := readInstallReplacementIdentity(image); ok {
		t.Fatal("changed argv accepted")
	}
	image.Cmdline = got.cmdline
	image.StartedAt = image.StartedAt.Add(-time.Second)
	if _, ok := readInstallReplacementIdentity(image); ok {
		t.Fatal("changed start accepted")
	}
}

func TestInstallReplacementStatBirthParsing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		payload string
		want    bool
	}{
		{"9 (name ) with spaces) S " + strings.Repeat("0 ", 18) + "1234 0", true},
		{"9 (name) S 0", false}, {"missing", false},
		{"9 (name) S " + strings.Repeat("0 ", 18) + "bad", false},
		{"9 (name) S " + strings.Repeat("0 ", 18) + "0", false},
	} {
		if _, ok := installReplacementStatBirth([]byte(tc.payload)); ok != tc.want {
			t.Fatalf("parse %q=%v", tc.payload, ok)
		}
	}
}

func TestInstallReplacementExplicitDomainAmbiguityNeverBorrowsHome(t *testing.T) {
	t.Parallel()
	broker := []string{"projmux", "internal", "codex-broker", "serve"}
	env := installReplacementEnviron{home: "/other/home"}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "absolute", args: []string{"--state-domain", "/this"}, want: installReplacementDomainThis},
		{name: "absent HOME fallback", want: installReplacementDomainOther},
		{name: "other then this", args: []string{"--state-domain", "/other", "--state-domain", "/this"}, want: installReplacementDomainUnknown},
		{name: "this then other", args: []string{"--state-domain=/this", "-state-domain=/other"}, want: installReplacementDomainUnknown},
		{name: "relative", args: []string{"--state-domain", "relative"}, want: installReplacementDomainUnknown},
		{name: "empty", args: []string{"--state-domain="}, want: installReplacementDomainUnknown},
		{name: "missing", args: []string{"--state-domain"}, want: installReplacementDomainUnknown},
		{name: "missing before flag", args: []string{"--state-domain", "--idle-timeout", "10m"}, want: installReplacementDomainUnknown},
		{name: "caller text excluded", args: []string{"--", "--state-domain", "/this"}, want: installReplacementDomainOther},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveInstallReplacementTargetOrigin("/this", append(slices.Clone(broker), tc.args...), env, true)
			if got.domain != tc.want {
				t.Fatalf("origin=%+v want %s", got, tc.want)
			}
			if tc.want == installReplacementDomainUnknown && got.stateDomain != "" {
				t.Fatal("ambiguous argv borrowed fallback")
			}
		})
	}
}
