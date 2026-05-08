package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func newStubDoctorCommand(host string, present map[string]bool) *doctorCommand {
	return &doctorCommand{
		lookPath: func(name string) (string, error) {
			if present[name] {
				return "/usr/bin/" + name, nil
			}
			return "", errors.New("not found")
		},
		goos:   func() string { return host },
		getenv: func(string) string { return "" },
		commandVersion: func(name string) string {
			if present[name] {
				switch name {
				case "tmux":
					return "tmux 3.6"
				case "fzf":
					return "0.71.0 (62899fd7)"
				}
				return name + " 1.2.3"
			}
			return ""
		},
	}
}

func TestDoctorRunAllRequiredPresentSucceeds(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("linux", map[string]bool{
		"tmux": true, "fzf": true, "git": true, "stty": true, "kubectl": true,
	})

	var stdout, stderr bytes.Buffer
	if err := cmd.Run(nil, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v\nstderr=%s", err, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"[ok]", "tmux", "fzf", "git", "stty", "kubectl"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q\nfull output:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "5 ok, 0 missing, 0 stale, 0 skipped, 0 hint.") {
		t.Fatalf("summary line wrong:\n%s", out)
	}
}

func TestDoctorRunRequiredMissingReturnsError(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("linux", map[string]bool{
		"tmux": true, "fzf": true, "stty": true, "apt-get": true,
	})

	var stdout, stderr bytes.Buffer
	err := cmd.Run(nil, &stdout, &stderr)
	if err == nil {
		t.Fatalf("Run() error = nil, want missing required failure")
	}
	out := stdout.String()
	if !strings.Contains(out, "[missing]") || !strings.Contains(out, "git") {
		t.Fatalf("output missing expected missing line:\n%s", out)
	}
	if !strings.Contains(out, "sudo apt-get install -y git") {
		t.Fatalf("apt-get install hint not rendered:\n%s", out)
	}
}

func TestDoctorInstallMissingDryRunPrintsCommands(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("linux", map[string]bool{
		"tmux": true, "fzf": true, "stty": true, "apt-get": true,
	})

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"--install-missing", "--dry-run"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "would install git: sudo apt-get install -y git") {
		t.Fatalf("dry-run install command missing:\n%s", out)
	}
}

func TestDoctorInstallMissingRunsCommands(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("linux", map[string]bool{
		"tmux": true, "fzf": true, "stty": true, "apt-get": true,
	})
	var ran []string
	cmd.runExternal = func(name string, args []string, stdout, stderr io.Writer) error {
		ran = append(ran, strings.Join(append([]string{name}, args...), " "))
		return nil
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"--install-missing"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := []string{"sudo apt-get install -y git"}
	if !equalStrings(ran, want) {
		t.Fatalf("ran = %#v, want %#v", ran, want)
	}
	if !strings.Contains(stdout.String(), "install commands completed; rerun projmux doctor to verify") {
		t.Fatalf("completion hint missing:\n%s", stdout.String())
	}
}

func TestDoctorInstallMissingCanIncludeOptional(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("darwin", map[string]bool{
		"tmux": true, "fzf": true, "git": true, "stty": true, "brew": true,
	})

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"--install-missing", "--include-optional", "--dry-run"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "would install kubectl: brew install kubectl") {
		t.Fatalf("optional install command missing:\n%s", out)
	}
}

func TestDoctorInstallFlagsRejectInvalidCombinations(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("linux", map[string]bool{})
	cases := []struct {
		args []string
		want string
	}{
		{args: []string{"--json", "--install-missing"}, want: "--json cannot be combined"},
		{args: []string{"--dry-run"}, want: "require --install-missing"},
		{args: []string{"--include-optional"}, want: "require --install-missing"},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			t.Parallel()
			err := cmd.Run(tc.args, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil {
				t.Fatalf("Run() error = nil, want %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Run() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestDoctorRunRejectsPositionalArguments(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("linux", map[string]bool{})
	err := cmd.Run([]string{"extra"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("Run() error = nil, want positional argument rejection")
	}
	if !strings.Contains(err.Error(), "positional") {
		t.Fatalf("Run() error = %v, want mention of positional arguments", err)
	}
}

func TestDoctorEvaluateOptionalMissingIsHintNotError(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("linux", map[string]bool{
		"tmux": true, "fzf": true, "git": true, "stty": true,
	})

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "[hint]") || !strings.Contains(out, "kubectl") {
		t.Fatalf("output missing kubectl hint line:\n%s", out)
	}
	if !strings.Contains(out, "optional; install if you use the kubectl switcher") {
		t.Fatalf("hint note not rendered:\n%s", out)
	}
	if !strings.Contains(out, "4 ok, 0 missing, 0 stale, 0 skipped, 1 hint.") {
		t.Fatalf("summary line wrong:\n%s", out)
	}
}

func TestDoctorEvaluateSkipsSttyOnWindows(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("windows", map[string]bool{
		"tmux": true, "fzf": true, "git": true, "kubectl": true,
	})

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "[skip]") || !strings.Contains(out, "stty") {
		t.Fatalf("stty should be skipped on windows:\n%s", out)
	}
	if !strings.Contains(out, "windows host") {
		t.Fatalf("skip reason missing:\n%s", out)
	}
}

func TestDoctorRunJSONOutputIsValid(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("linux", map[string]bool{
		"tmux": true, "fzf": true, "git": true, "stty": true,
	})

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"--json"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	var results []doctorResult
	if err := json.Unmarshal(stdout.Bytes(), &results); err != nil {
		t.Fatalf("json.Unmarshal error = %v\noutput=%s", err, stdout.String())
	}
	if len(results) != 5 {
		t.Fatalf("len(results) = %d, want 5", len(results))
	}
	byName := map[string]doctorResult{}
	for _, r := range results {
		byName[r.Name] = r
	}
	if byName["tmux"].Status != doctorStatusOK {
		t.Fatalf("tmux status = %q, want ok", byName["tmux"].Status)
	}
	if byName["kubectl"].Status != doctorStatusHint {
		t.Fatalf("kubectl status = %q, want hint", byName["kubectl"].Status)
	}
	if !byName["tmux"].Required {
		t.Fatalf("tmux Required = false, want true")
	}
	if byName["kubectl"].Required {
		t.Fatalf("kubectl Required = true, want false")
	}
}

func TestDetectInstallHintByOSAndPM(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		dep      doctorDep
		host     string
		present  map[string]bool
		want     string
		contains []string
	}{
		{
			name:    "linux apt-get",
			dep:     doctorDep{Name: "git"},
			host:    "linux",
			present: map[string]bool{"apt-get": true},
			want:    "sudo apt-get install -y git",
		},
		{
			name:    "linux pacman",
			dep:     doctorDep{Name: "git"},
			host:    "linux",
			present: map[string]bool{"pacman": true},
			want:    "sudo pacman -S git",
		},
		{
			name:    "linux dnf",
			dep:     doctorDep{Name: "git"},
			host:    "linux",
			present: map[string]bool{"dnf": true},
			want:    "sudo dnf install git",
		},
		{
			name:    "linux zypper",
			dep:     doctorDep{Name: "git"},
			host:    "linux",
			present: map[string]bool{"zypper": true},
			want:    "sudo zypper install git",
		},
		{
			name:    "linux apk",
			dep:     doctorDep{Name: "git"},
			host:    "linux",
			present: map[string]bool{"apk": true},
			want:    "sudo apk add git",
		},
		{
			name:    "darwin brew",
			dep:     doctorDep{Name: "git"},
			host:    "darwin",
			present: map[string]bool{"brew": true},
			want:    "brew install git",
		},
		{
			name:    "windows scoop default",
			dep:     doctorDep{Name: "git"},
			host:    "windows",
			present: map[string]bool{},
			want:    "scoop install git",
		},
		{
			name:    "manual install hint suppresses linux apt command",
			dep:     doctorDep{Name: "fzf", ManualInstallHint: "install the junegunn/fzf CLI >= 0.65.0"},
			host:    "linux",
			present: map[string]bool{"apt-get": true},
			want:    "manual: install the junegunn/fzf CLI >= 0.65.0",
		},
		{
			name:    "manual install hint works without package manager",
			dep:     doctorDep{Name: "fzf", ManualInstallHint: "install the junegunn/fzf CLI >= 0.65.0"},
			host:    "linux",
			present: map[string]bool{},
			want:    "manual: install the junegunn/fzf CLI >= 0.65.0",
		},
		{
			name:    "linux no package manager returns empty",
			dep:     doctorDep{Name: "git"},
			host:    "linux",
			present: map[string]bool{},
			want:    "",
		},
		{
			name:    "darwin without brew returns empty",
			dep:     doctorDep{Name: "git"},
			host:    "darwin",
			present: map[string]bool{},
			want:    "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lookPath := func(name string) (string, error) {
				if tc.present[name] {
					return "/usr/bin/" + name, nil
				}
				return "", errors.New("not found")
			}
			got := detectInstallHint(tc.dep, tc.host, lookPath)
			if len(tc.contains) > 0 {
				for _, want := range tc.contains {
					if !strings.Contains(got, want) {
						t.Fatalf("detectInstallHint = %q, want substring %q", got, want)
					}
				}
				return
			}
			if got != tc.want {
				t.Fatalf("detectInstallHint = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDoctorFzfHintIsManualCLIGuidance(t *testing.T) {
	t.Parallel()

	hosts := []struct {
		name    string
		host    string
		present map[string]bool
	}{
		{"linux apt", "linux", map[string]bool{"apt-get": true}},
		{"linux pacman", "linux", map[string]bool{"pacman": true}},
		{"darwin brew", "darwin", map[string]bool{"brew": true}},
		{"windows", "windows", map[string]bool{}},
		{"linux bare", "linux", map[string]bool{}},
	}

	deps := doctorDeps()
	var fzfDep doctorDep
	for _, d := range deps {
		if d.Name == "fzf" {
			fzfDep = d
			break
		}
	}
	if fzfDep.Name == "" {
		t.Fatalf("fzf dep not present in doctorDeps()")
	}

	for _, tc := range hosts {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lookPath := func(name string) (string, error) {
				if tc.present[name] {
					return "/usr/bin/" + name, nil
				}
				return "", errors.New("not found")
			}
			got := detectInstallHint(fzfDep, tc.host, lookPath)
			for _, want := range []string{"manual:", "junegunn/fzf CLI", "0.65.0", "`fzf --version`", "`npm i fzf`"} {
				if !strings.Contains(got, want) {
					t.Fatalf("fzf hint on %s missing %q: %q", tc.name, want, got)
				}
			}
			if strings.Contains(got, "apt-get install") {
				t.Fatalf("fzf hint should not imply apt installs a sufficient version: %q", got)
			}
		})
	}
}

func TestDoctorRunWindowsMissingHintIncludesScoop(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("windows", map[string]bool{
		"tmux": true, "fzf": true, "kubectl": true,
	})

	var stdout bytes.Buffer
	err := cmd.Run(nil, &stdout, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("Run() error = nil, want missing required (git) on windows")
	}
	if !strings.Contains(stdout.String(), "scoop install git") {
		t.Fatalf("windows install hint missing scoop:\n%s", stdout.String())
	}
}

func TestDoctorRunDarwinMissingHintIncludesBrew(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("darwin", map[string]bool{
		"tmux": true, "fzf": true, "stty": true, "brew": true,
	})

	var stdout bytes.Buffer
	err := cmd.Run(nil, &stdout, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("Run() error = nil, want missing required (git) on darwin")
	}
	if !strings.Contains(stdout.String(), "brew install git") {
		t.Fatalf("darwin install hint missing brew:\n%s", stdout.String())
	}
}

func TestDoctorRunLinuxPacmanMissingHint(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommand("linux", map[string]bool{
		"tmux": true, "fzf": true, "stty": true, "pacman": true,
	})

	var stdout bytes.Buffer
	err := cmd.Run(nil, &stdout, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("Run() error = nil, want missing required (git)")
	}
	if !strings.Contains(stdout.String(), "sudo pacman -S git") {
		t.Fatalf("pacman install hint not rendered:\n%s", stdout.String())
	}
}

func newStubDoctorCommandWithVersions(host string, present map[string]bool, versions map[string]string) *doctorCommand {
	return &doctorCommand{
		lookPath: func(name string) (string, error) {
			if present[name] {
				return "/usr/bin/" + name, nil
			}
			return "", errors.New("not found")
		},
		goos:   func() string { return host },
		getenv: func(string) string { return "" },
		commandVersion: func(name string) string {
			if v, ok := versions[name]; ok {
				return v
			}
			if !present[name] {
				return ""
			}
			switch name {
			case "tmux":
				return "tmux 3.6"
			case "fzf":
				return "0.71.0 (62899fd7)"
			}
			return name + " 1.2.3"
		},
	}
}

func TestParseDoctorVersion(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		in    string
		major int
		minor int
		patch int
		ok    bool
	}{
		{"tmux 3.6", "tmux 3.6", 3, 6, 0, true},
		{"tmux 3.4a", "tmux 3.4a", 3, 4, 0, true},
		{"plain 3.4", "3.4", 3, 4, 0, true},
		{"fzf full", "0.71.0 (62899fd7)", 0, 71, 0, true},
		{"fzf devel", "0.54 (devel)", 0, 54, 0, true},
		{"git long", "git version 2.53.0", 2, 53, 0, true},
		{"empty", "", 0, 0, 0, false},
		{"unrecognized", "unrecognized", 0, 0, 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			major, minor, patch, ok := parseDoctorVersion(tc.in)
			if major != tc.major || minor != tc.minor || patch != tc.patch || ok != tc.ok {
				t.Fatalf("parseDoctorVersion(%q) = (%d, %d, %d, %v), want (%d, %d, %d, %v)",
					tc.in, major, minor, patch, ok, tc.major, tc.minor, tc.patch, tc.ok)
			}
		})
	}
}

func TestVersionAtLeast(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		got     string
		want    string
		atLeast bool
		parsed  bool
	}{
		{"3.6 >= 3.4", "3.6", "3.4", true, true},
		{"3.4 >= 3.4", "3.4", "3.4", true, true},
		{"3.3 < 3.4", "3.3", "3.4", false, true},
		{"3.4a >= 3.4", "3.4a", "3.4", true, true},
		{"0.65.0 >= 0.65.0", "0.65.0", "0.65.0", true, true},
		{"0.64.0 < 0.65.0", "0.64.0", "0.65.0", false, true},
		{"0.71.0 >= 0.65.0", "0.71.0", "0.65.0", true, true},
		{"garbage lenient", "garbage", "0.65.0", true, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			atLeast, parsed := versionAtLeast(tc.got, tc.want)
			if atLeast != tc.atLeast || parsed != tc.parsed {
				t.Fatalf("versionAtLeast(%q, %q) = (%v, %v), want (%v, %v)",
					tc.got, tc.want, atLeast, parsed, tc.atLeast, tc.parsed)
			}
		})
	}
}

func TestDoctorStaleFzfFailsRequired(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommandWithVersions(
		"linux",
		map[string]bool{"tmux": true, "fzf": true, "git": true, "stty": true, "kubectl": true, "apt-get": true},
		map[string]string{"fzf": "fzf 0.64.0 (distro build)"},
	)

	var stdout, stderr bytes.Buffer
	err := cmd.Run(nil, &stdout, &stderr)
	if err == nil {
		t.Fatalf("Run() error = nil, want stale-required failure")
	}
	if !strings.Contains(err.Error(), "missing required dependencies") {
		t.Fatalf("error = %v, want mention of missing required dependencies", err)
	}
	out := stdout.String()
	for _, want := range []string{"[stale]", "fzf", "minimum 0.65.0; found fzf 0.64.0 (distro build)", "junegunn/fzf CLI", "`npm i fzf`"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q\nfull output:\n%s", want, out)
		}
	}
	if strings.Contains(out, "sudo apt-get install -y fzf") {
		t.Fatalf("stale fzf guidance should not imply apt install is enough:\n%s", out)
	}
	if !strings.Contains(out, "1 stale") {
		t.Fatalf("summary line missing stale count:\n%s", out)
	}
}

func TestDoctorInstallMissingStaleFzfRequiresManualInstall(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommandWithVersions(
		"linux",
		map[string]bool{"tmux": true, "fzf": true, "git": true, "stty": true, "kubectl": true, "apt-get": true},
		map[string]string{"fzf": "0.64.0 (Ubuntu package)"},
	)

	var stdout bytes.Buffer
	err := cmd.Run([]string{"--install-missing", "--dry-run"}, &stdout, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("Run() error = nil, want manual install failure")
	}
	out := stdout.String()
	for _, want := range []string{
		"manual install required for fzf",
		"junegunn/fzf CLI executable >= 0.65.0",
		"Ubuntu 24 apt may be too old",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q\nfull output:\n%s", want, out)
		}
	}
	if strings.Contains(out, "would install fzf: sudo apt-get install") {
		t.Fatalf("install-missing should not dry-run apt for stale fzf:\n%s", out)
	}
	if !strings.Contains(err.Error(), "manual install required for fzf") {
		t.Fatalf("Run() error = %v, want manual install mention", err)
	}
}

func TestDoctorStaleTmuxFailsRequired(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommandWithVersions(
		"linux",
		map[string]bool{"tmux": true, "fzf": true, "git": true, "stty": true, "kubectl": true, "apt-get": true},
		map[string]string{"tmux": "tmux 3.2"},
	)

	var stdout, stderr bytes.Buffer
	err := cmd.Run(nil, &stdout, &stderr)
	if err == nil {
		t.Fatalf("Run() error = nil, want stale-required failure")
	}
	if !strings.Contains(err.Error(), "missing required dependencies") {
		t.Fatalf("error = %v, want mention of missing required dependencies", err)
	}
	out := stdout.String()
	for _, want := range []string{"[stale]", "tmux", "minimum 3.4; found tmux 3.2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q\nfull output:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "1 stale") {
		t.Fatalf("summary line missing stale count:\n%s", out)
	}
}

func TestDoctorNoMinVersionSkipsCheck(t *testing.T) {
	t.Parallel()

	// Confirms that when MinVersion == "" (e.g. git, stty, kubectl), no
	// version comparison happens even if the version output is empty.
	cmd := newStubDoctorCommandWithVersions(
		"linux",
		map[string]bool{"tmux": true, "fzf": true, "git": true, "stty": true, "kubectl": true},
		map[string]string{"git": "", "stty": "", "kubectl": ""},
	)

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v\noutput=%s", err, stdout.String())
	}
	out := stdout.String()
	if strings.Contains(out, "[stale]") {
		t.Fatalf("output should not flag any dep as stale:\n%s", out)
	}
	if !strings.Contains(out, "5 ok, 0 missing, 0 stale, 0 skipped, 0 hint.") {
		t.Fatalf("summary line wrong:\n%s", out)
	}
}

func TestDoctorVersionParseFailureDoesNotMarkStale(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommandWithVersions(
		"linux",
		map[string]bool{"tmux": true, "fzf": true, "git": true, "stty": true, "kubectl": true},
		map[string]string{"fzf": ""},
	)

	results := cmd.evaluate()
	var fzf doctorResult
	for _, r := range results {
		if r.Name == "fzf" {
			fzf = r
			break
		}
	}
	if fzf.Name == "" {
		t.Fatalf("fzf result missing")
	}
	if fzf.Status != doctorStatusOK {
		t.Fatalf("fzf status = %q, want ok (parse glitch should not mark stale)", fzf.Status)
	}
}

func TestDoctorStaleFzfSerializesToJSON(t *testing.T) {
	t.Parallel()

	cmd := newStubDoctorCommandWithVersions(
		"linux",
		map[string]bool{"tmux": true, "fzf": true, "git": true, "stty": true, "kubectl": true, "apt-get": true},
		map[string]string{"fzf": "0.64.0 (devel)"},
	)

	var stdout bytes.Buffer
	// Run returns the stale-required error but JSON output is still written
	// to stdout before the error return path checks status.
	_ = cmd.Run([]string{"--json"}, &stdout, &bytes.Buffer{})

	var results []doctorResult
	if err := json.Unmarshal(stdout.Bytes(), &results); err != nil {
		t.Fatalf("json.Unmarshal error = %v\noutput=%s", err, stdout.String())
	}
	byName := map[string]doctorResult{}
	for _, r := range results {
		byName[r.Name] = r
	}
	fzf, ok := byName["fzf"]
	if !ok {
		t.Fatalf("fzf result missing from JSON output")
	}
	if fzf.Status != doctorStatusStale {
		t.Fatalf("fzf JSON status = %q, want stale", fzf.Status)
	}
	if fzf.Hint == "" {
		t.Fatalf("fzf JSON hint unset, want minimum/found message")
	}
	if !strings.Contains(string(stdout.Bytes()), `"status": "stale"`) {
		t.Fatalf("raw JSON missing %q:\n%s", `"status": "stale"`, stdout.String())
	}
}
