package app

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUpgradeNormalizeUpgradeRef(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "default latest", input: "latest", want: "latest"},
		{name: "version tag", input: "v0.2.0", want: "v0.2.0"},
		{name: "branch", input: "main", want: "main"},
		{name: "leading at strip", input: "@v1", want: "v1"},
		{name: "trim whitespace", input: "  v1  ", want: "v1"},
		{name: "empty", input: "", wantErr: true},
		{name: "whitespace only", input: "   ", wantErr: true},
		{name: "internal whitespace", input: "v 1", wantErr: true},
		{name: "embedded at", input: "module@v1", wantErr: true},
		{name: "only at", input: "@", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := normalizeUpgradeRef(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("normalizeUpgradeRef(%q) error = nil, want error", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeUpgradeRef(%q) error = %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("normalizeUpgradeRef(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestUpgradeRunDryRunPrintsExpectedCommands(t *testing.T) {
	t.Parallel()

	cmd := newStubUpgradeCommand("/home/user/.local/bin/projmux")

	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"--dry-run"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	out := stdout.String()
	wantSubstrings := []string{
		"would run: GOBIN=/home/user/.local/bin/.projmux-upgrade-XXXXXX go install github.com/crevissepartners/projmux/cmd/projmux@latest",
		"would replace: /home/user/.local/bin/projmux (atomic via temp file)",
		"would run: /home/user/.local/bin/projmux tmux apply",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(out, want) {
			t.Fatalf("dry-run output missing %q\nfull output:\n%s", want, out)
		}
	}
}

func TestUpgradeRunDryRunWithFlags(t *testing.T) {
	t.Parallel()

	cmd := newStubUpgradeCommand("/home/user/.local/bin/projmux")

	var stdout bytes.Buffer
	args := []string{
		"--dry-run",
		"--ref", "v0.2.0",
		"--module", "github.com/example/fork/cmd/projmux",
		"--target", "/home/user/.local/bin/projmux",
		"--no-apply",
	}
	if err := cmd.Run(args, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "go install github.com/example/fork/cmd/projmux@v0.2.0") {
		t.Fatalf("dry-run output missing custom module/ref:\n%s", out)
	}
	if strings.Contains(out, "tmux apply") {
		t.Fatalf("dry-run output should not mention tmux apply when --no-apply set:\n%s", out)
	}
}

func TestUpgradeRunDryRunStripsLeadingAtInRef(t *testing.T) {
	t.Parallel()

	cmd := newStubUpgradeCommand("/home/user/.local/bin/projmux")

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"--dry-run", "--ref", "@latest"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(stdout.String(), "@latest") {
		t.Fatalf("dry-run output missing normalized ref:\n%s", stdout.String())
	}
	if strings.Contains(stdout.String(), "@@latest") {
		t.Fatalf("dry-run leaked double @ in ref:\n%s", stdout.String())
	}
}

func TestUpgradeRunRejectsInvalidRef(t *testing.T) {
	t.Parallel()

	cmd := newStubUpgradeCommand("/home/user/.local/bin/projmux")

	var stderr bytes.Buffer
	err := cmd.Run([]string{"--ref", " "}, &bytes.Buffer{}, &stderr)
	if err == nil {
		t.Fatalf("Run() error = nil, want error for empty ref")
	}
	if !strings.Contains(err.Error(), "--ref") {
		t.Fatalf("Run() error = %v, want mention of --ref", err)
	}
}

func TestUpgradeRunRejectsPositionalArguments(t *testing.T) {
	t.Parallel()

	cmd := newStubUpgradeCommand("/home/user/.local/bin/projmux")

	err := cmd.Run([]string{"--dry-run", "extra"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("Run() error = nil, want positional argument rejection")
	}
	if !strings.Contains(err.Error(), "positional") {
		t.Fatalf("Run() error = %v, want mention of positional arguments", err)
	}
}

func TestUpgradeRunResolvesTargetViaEvalSymlinks(t *testing.T) {
	t.Parallel()

	cmd := newStubUpgradeCommand("/home/user/.local/bin/projmux")
	cmd.evalSymlinks = func(path string) (string, error) {
		if path == "/home/user/.local/bin/projmux" {
			return "/opt/projmux/bin/projmux", nil
		}
		return path, nil
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"--dry-run"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(stdout.String(), "would replace: /opt/projmux/bin/projmux") {
		t.Fatalf("dry-run did not honor EvalSymlinks resolution:\n%s", stdout.String())
	}
}

func TestUpgradeRunResolvesTargetOverrideToAbsolutePath(t *testing.T) {
	t.Parallel()

	cmd := newStubUpgradeCommand("/should/not/be/used")
	cmd.evalSymlinks = func(path string) (string, error) { return path, nil }

	var stdout bytes.Buffer
	args := []string{"--dry-run", "--target", "relative/path/projmux"}
	if err := cmd.Run(args, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd() error = %v", err)
	}
	wantTarget := filepath.Join(wd, "relative/path/projmux")
	if !strings.Contains(stdout.String(), "would replace: "+wantTarget) {
		t.Fatalf("dry-run did not absolutize --target:\nout=%s\nwant target=%s", stdout.String(), wantTarget)
	}
}

func TestUpgradeRunReportsMissingGoToolchain(t *testing.T) {
	t.Parallel()

	cmd := newStubUpgradeCommand("/home/user/.local/bin/projmux")
	cmd.lookPath = func(string) (string, error) {
		return "", errors.New("not found")
	}

	err := cmd.Run([]string{}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("Run() error = nil, want missing go toolchain error")
	}
	if !strings.Contains(err.Error(), "'go' toolchain") {
		t.Fatalf("Run() error = %v, want hint about missing go toolchain", err)
	}
}

func TestUpgradeRunHappyPathInvokesGoInstallAndApply(t *testing.T) {
	t.Parallel()

	target := "/home/user/.local/bin/projmux"
	cmd := newStubUpgradeCommand(target)

	stub := newUpgradeRecorder(target)
	cmd.lookPath = func(name string) (string, error) {
		if name != "go" {
			return "", fmt.Errorf("unexpected lookPath %q", name)
		}
		return "/usr/local/bin/go", nil
	}
	cmd.mkdirTemp = func(dir, pattern string) (string, error) {
		if dir != filepath.Dir(target) {
			return "", fmt.Errorf("mkdirTemp dir = %q, want %q", dir, filepath.Dir(target))
		}
		return "/home/user/.local/bin/.projmux-upgrade-stub", nil
	}
	cmd.removeAll = stub.removeAll
	cmd.remove = stub.remove
	cmd.stat = stub.stat
	cmd.rename = stub.rename
	cmd.chmod = stub.chmod
	cmd.environ = func() []string { return []string{"PATH=/usr/bin", "GOBIN=/old/gobin"} }
	cmd.newCmd = stub.newCmd
	cmd.runCmd = stub.runCmd
	cmd.tempSuffixGen = func() string { return "12345" }

	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"--ref", "v1.2.3"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v\nstderr=%s", err, stderr.String())
	}

	if len(stub.commands) != 2 {
		t.Fatalf("commands = %d, want 2 (go install + apply)\nrecorded=%v", len(stub.commands), stub.commands)
	}

	wantInstall := []string{"/usr/local/bin/go", "install", "github.com/crevissepartners/projmux/cmd/projmux@v1.2.3"}
	if !equalSlices(stub.commands[0].args, wantInstall) {
		t.Fatalf("install command = %v, want %v", stub.commands[0].args, wantInstall)
	}
	if !envContains(stub.commands[0].env, "GOBIN=/home/user/.local/bin/.projmux-upgrade-stub") {
		t.Fatalf("install env missing scratch GOBIN:\n%v", stub.commands[0].env)
	}
	if envContains(stub.commands[0].env, "GOBIN=/old/gobin") {
		t.Fatalf("install env still contains stale GOBIN:\n%v", stub.commands[0].env)
	}

	wantApply := []string{target, "tmux", "apply"}
	if !equalSlices(stub.commands[1].args, wantApply) {
		t.Fatalf("apply command = %v, want %v", stub.commands[1].args, wantApply)
	}

	wantRenames := [][2]string{
		{"/home/user/.local/bin/.projmux-upgrade-stub/projmux", target + ".upgrade.12345"},
		{target + ".upgrade.12345", target},
	}
	if !equalRenames(stub.renames, wantRenames) {
		t.Fatalf("renames = %v, want %v", stub.renames, wantRenames)
	}
	if stub.chmodPath != target+".upgrade.12345" || stub.chmodMode != 0o755 {
		t.Fatalf("chmod = (%q, %#o), want (%q, %#o)", stub.chmodPath, stub.chmodMode, target+".upgrade.12345", 0o755)
	}

	out := stdout.String()
	for _, want := range []string{
		">> fetching github.com/crevissepartners/projmux/cmd/projmux@v1.2.3 via go install",
		">> installed: /home/user/.local/bin/.projmux-upgrade-stub/projmux",
		">> atomically replaced " + target,
		">> applying live config...",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout missing %q\nfull output:\n%s", want, out)
		}
	}
}

func TestUpgradeRunHappyPathSkipsApplyWhenNoApplySet(t *testing.T) {
	t.Parallel()

	target := "/home/user/.local/bin/projmux"
	cmd := newStubUpgradeCommand(target)

	stub := newUpgradeRecorder(target)
	cmd.lookPath = func(string) (string, error) { return "/usr/local/bin/go", nil }
	cmd.mkdirTemp = func(string, string) (string, error) {
		return "/home/user/.local/bin/.projmux-upgrade-stub", nil
	}
	cmd.removeAll = stub.removeAll
	cmd.remove = stub.remove
	cmd.stat = stub.stat
	cmd.rename = stub.rename
	cmd.chmod = stub.chmod
	cmd.environ = func() []string { return []string{} }
	cmd.newCmd = stub.newCmd
	cmd.runCmd = stub.runCmd
	cmd.tempSuffixGen = func() string { return "stub" }

	if err := cmd.Run([]string{"--no-apply"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(stub.commands) != 1 {
		t.Fatalf("commands = %d, want 1 (go install only)\nrecorded=%v", len(stub.commands), stub.commands)
	}
}

func TestUpgradeRunPropagatesGoInstallFailureAndCleansUp(t *testing.T) {
	t.Parallel()

	target := "/home/user/.local/bin/projmux"
	cmd := newStubUpgradeCommand(target)

	stub := newUpgradeRecorder(target)
	cmd.lookPath = func(string) (string, error) { return "/usr/local/bin/go", nil }
	cmd.mkdirTemp = func(string, string) (string, error) {
		return "/home/user/.local/bin/.projmux-upgrade-stub", nil
	}
	cmd.removeAll = stub.removeAll
	cmd.environ = func() []string { return []string{} }
	cmd.newCmd = stub.newCmd
	cmd.runCmd = func(c *exec.Cmd) error {
		stub.recordCmd(c)
		return fmt.Errorf("fake network failure")
	}

	err := cmd.Run([]string{}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("Run() error = nil, want propagated install failure")
	}
	if !strings.Contains(err.Error(), "go install") {
		t.Fatalf("Run() error = %v, want go install context", err)
	}
	if len(stub.removed) != 1 || stub.removed[0] != "/home/user/.local/bin/.projmux-upgrade-stub" {
		t.Fatalf("removeAll calls = %v, want cleanup of scratch directory", stub.removed)
	}
}

// --- helpers ---

func newStubUpgradeCommand(target string) *upgradeCommand {
	return &upgradeCommand{
		executable:    func() (string, error) { return target, nil },
		evalSymlinks:  func(path string) (string, error) { return path, nil },
		lookPath:      func(string) (string, error) { return "/usr/local/bin/go", nil },
		mkdirTemp:     func(dir, pattern string) (string, error) { return filepath.Join(dir, ".projmux-upgrade-stub"), nil },
		rename:        func(string, string) error { return nil },
		chmod:         func(string, os.FileMode) error { return nil },
		stat:          func(string) (os.FileInfo, error) { return stubFileInfo{name: "projmux"}, nil },
		removeAll:     func(string) error { return nil },
		remove:        func(string) error { return nil },
		environ:       func() []string { return []string{} },
		runCmd:        func(*exec.Cmd) error { return nil },
		newCmd:        exec.Command,
		copyFile:      func(string, string) error { return nil },
		tempSuffixGen: func() string { return "stub" },
	}
}

type recordedCmd struct {
	args []string
	env  []string
}

type upgradeRecorder struct {
	mu        sync.Mutex
	commands  []recordedCmd
	renames   [][2]string
	removed   []string
	removes   []string
	chmodPath string
	chmodMode os.FileMode
	target    string
}

func newUpgradeRecorder(target string) *upgradeRecorder {
	return &upgradeRecorder{target: target}
}

func (r *upgradeRecorder) newCmd(name string, args ...string) *exec.Cmd {
	full := append([]string{name}, args...)
	return &exec.Cmd{Path: name, Args: full}
}

func (r *upgradeRecorder) recordCmd(c *exec.Cmd) {
	r.mu.Lock()
	defer r.mu.Unlock()
	env := append([]string(nil), c.Env...)
	r.commands = append(r.commands, recordedCmd{args: append([]string(nil), c.Args...), env: env})
}

func (r *upgradeRecorder) runCmd(c *exec.Cmd) error {
	r.recordCmd(c)
	return nil
}

func (r *upgradeRecorder) rename(oldpath, newpath string) error {
	r.mu.Lock()
	r.renames = append(r.renames, [2]string{oldpath, newpath})
	r.mu.Unlock()
	return nil
}

func (r *upgradeRecorder) chmod(path string, mode os.FileMode) error {
	r.mu.Lock()
	r.chmodPath = path
	r.chmodMode = mode
	r.mu.Unlock()
	return nil
}

func (r *upgradeRecorder) removeAll(path string) error {
	r.mu.Lock()
	r.removed = append(r.removed, path)
	r.mu.Unlock()
	return nil
}

func (r *upgradeRecorder) remove(path string) error {
	r.mu.Lock()
	r.removes = append(r.removes, path)
	r.mu.Unlock()
	return nil
}

func (r *upgradeRecorder) stat(path string) (os.FileInfo, error) {
	return stubFileInfo{name: filepath.Base(path)}, nil
}

type stubFileInfo struct {
	name string
}

func (s stubFileInfo) Name() string     { return s.name }
func (stubFileInfo) Size() int64        { return 0 }
func (stubFileInfo) Mode() os.FileMode  { return 0o755 }
func (stubFileInfo) ModTime() time.Time { return time.Time{} }
func (stubFileInfo) IsDir() bool        { return false }
func (stubFileInfo) Sys() any           { return nil }

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalRenames(a, b [][2]string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func envContains(env []string, want string) bool {
	return slices.Contains(env, want)
}
