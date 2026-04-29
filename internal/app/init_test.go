package app

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeAdapter is a minimal TerminalAdapter used by registry/dispatch tests.
type fakeAdapter struct {
	name        string
	detect      bool
	detectCalls int
	planFn      func(current string, exists bool) (MergePlan, error)
	applied     *MergePlan
	configPath  string
}

func (f *fakeAdapter) Name() string { return f.name }

func (f *fakeAdapter) Detect(env func(string) string) bool {
	f.detectCalls++
	return f.detect
}

func (f *fakeAdapter) ConfigPath(env func(string) string) (string, error) {
	if f.configPath == "" {
		return "/tmp/" + f.name + "/config", nil
	}
	return f.configPath, nil
}

func (f *fakeAdapter) PlanMerge(current string, exists bool) (MergePlan, error) {
	if f.planFn != nil {
		return f.planFn(current, exists)
	}
	return MergePlan{Original: current, Updated: current + "added\n", CreateNew: !exists,
		Changes: []MergeChange{{Trigger: "alt+1", Action: "csi:9005u", Kind: "add"}}}, nil
}

func (f *fakeAdapter) ApplyMerge(plan MergePlan) error {
	cp := plan
	f.applied = &cp
	return nil
}

func newTestRegistry() *terminalRegistry {
	return &terminalRegistry{adapters: map[string]TerminalAdapter{}}
}

func TestTerminalRegistryRegisterAndLookup(t *testing.T) {
	t.Parallel()

	reg := newTestRegistry()
	a := &fakeAdapter{name: "alpha"}
	reg.register(a)

	got, ok := reg.lookup("alpha")
	if !ok || got != a {
		t.Fatalf("lookup(alpha) = (%v, %v), want adapter %v", got, ok, a)
	}
	if _, ok := reg.lookup("missing"); ok {
		t.Fatalf("lookup(missing) ok = true, want false")
	}

	names := reg.names()
	if len(names) != 1 || names[0] != "alpha" {
		t.Fatalf("names() = %v, want [alpha]", names)
	}
}

func TestTerminalRegistryRegisterDuplicatePanics(t *testing.T) {
	t.Parallel()

	reg := newTestRegistry()
	reg.register(&fakeAdapter{name: "alpha"})
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on duplicate register")
		}
	}()
	reg.register(&fakeAdapter{name: "alpha"})
}

func TestTerminalRegistryRegisterEmptyNamePanics(t *testing.T) {
	t.Parallel()

	reg := newTestRegistry()
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on empty name")
		}
	}()
	reg.register(&fakeAdapter{name: ""})
}

func TestTerminalRegistryDetectReturnsFirstMatch(t *testing.T) {
	t.Parallel()

	reg := newTestRegistry()
	reg.register(&fakeAdapter{name: "alpha"})
	beta := &fakeAdapter{name: "beta", detect: true}
	reg.register(beta)
	reg.register(&fakeAdapter{name: "gamma", detect: true})

	got, ok := reg.detect(func(string) string { return "" })
	if !ok || got.Name() != "beta" {
		t.Fatalf("detect() = (%v, %v); want beta adapter", got, ok)
	}
}

func TestTerminalRegistryDetectNoMatch(t *testing.T) {
	t.Parallel()

	reg := newTestRegistry()
	reg.register(&fakeAdapter{name: "alpha"})

	if got, ok := reg.detect(func(string) string { return "" }); ok {
		t.Fatalf("detect() ok = true (got %v); want false", got)
	}
}

func TestRegisterTerminalAdapterRegistersGhosttyByDefault(t *testing.T) {
	t.Parallel()

	got, ok := defaultTerminalRegistry.lookup("ghostty")
	if !ok {
		t.Fatalf("default registry missing ghostty adapter")
	}
	if got.Name() != "ghostty" {
		t.Fatalf("default ghostty adapter name = %q, want ghostty", got.Name())
	}
}

func TestInitCommandUnknownTerminalReturnsError(t *testing.T) {
	t.Parallel()

	reg := newTestRegistry()
	reg.register(&fakeAdapter{name: "alpha"})

	cmd := &initCommand{registry: reg, getenv: func(string) string { return "" }}
	var stdout, stderr bytes.Buffer
	err := cmd.Run([]string{"unknown"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "unknown terminal") {
		t.Fatalf("Run(unknown) error = %v, want unknown terminal", err)
	}
}

func TestInitCommandAutoDetectFailsWithoutMatch(t *testing.T) {
	t.Parallel()

	reg := newTestRegistry()
	reg.register(&fakeAdapter{name: "alpha"})

	cmd := &initCommand{registry: reg, getenv: func(string) string { return "" }}
	var stdout, stderr bytes.Buffer
	err := cmd.Run(nil, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "auto-detect") {
		t.Fatalf("Run() error = %v, want auto-detect failure", err)
	}
}

func TestInitCommandDryRunPrintsPlanAndDoesNotApply(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "config")
	if err := os.WriteFile(cfg, []byte("# user config\n"), 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	adapter := &fakeAdapter{
		name:       "alpha",
		detect:     true,
		configPath: cfg,
	}
	reg := newTestRegistry()
	reg.register(adapter)

	cmd := &initCommand{
		registry: reg,
		getenv:   func(string) string { return "" },
		readFile: os.ReadFile,
		stat:     os.Stat,
	}
	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"alpha"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v (stderr=%s)", err, stderr.String())
	}
	if adapter.applied != nil {
		t.Fatalf("dry-run unexpectedly applied: %+v", adapter.applied)
	}
	out := stdout.String()
	if !strings.Contains(out, "dry-run") {
		t.Fatalf("dry-run output missing 'dry-run' marker: %q", out)
	}
	if !strings.Contains(out, "--apply") {
		t.Fatalf("dry-run output missing apply hint: %q", out)
	}
	if !strings.Contains(out, "alt+1") {
		t.Fatalf("dry-run output missing change preview: %q", out)
	}
}

func TestInitCommandApplyInvokesAdapter(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "config")

	adapter := &fakeAdapter{name: "alpha", configPath: cfg}
	reg := newTestRegistry()
	reg.register(adapter)

	cmd := &initCommand{
		registry: reg,
		getenv:   func(string) string { return "" },
		readFile: os.ReadFile,
		stat:     os.Stat,
	}
	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"alpha", "--apply"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run(--apply) error = %v (stderr=%s)", err, stderr.String())
	}
	if adapter.applied == nil {
		t.Fatalf("--apply did not call ApplyMerge")
	}
	if adapter.applied.ConfigPath != cfg {
		t.Fatalf("ApplyMerge plan.ConfigPath = %q, want %q", adapter.applied.ConfigPath, cfg)
	}
	if !strings.Contains(stdout.String(), "wrote") {
		t.Fatalf("apply stdout = %q, want 'wrote' summary", stdout.String())
	}
}

func TestInitCommandRejectsApplyAndDryRunTogether(t *testing.T) {
	t.Parallel()

	cmd := &initCommand{
		registry: newTestRegistry(),
		getenv:   func(string) string { return "" },
	}
	var stdout, stderr bytes.Buffer
	err := cmd.Run([]string{"--apply", "--dry-run"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("Run(--apply --dry-run) error = %v, want mutual exclusion", err)
	}
}

func TestInitCommandLoadConfigMissingFile(t *testing.T) {
	t.Parallel()

	cmd := &initCommand{
		readFile: os.ReadFile,
		stat: func(name string) (os.FileInfo, error) {
			return nil, os.ErrNotExist
		},
	}
	current, exists, err := cmd.loadConfig("/nope")
	if err != nil {
		t.Fatalf("loadConfig missing error = %v", err)
	}
	if exists {
		t.Fatalf("loadConfig missing exists = true, want false")
	}
	if current != "" {
		t.Fatalf("loadConfig missing current = %q, want empty", current)
	}
}

func TestInitCommandLoadConfigPropagatesUnexpectedStatError(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("boom")
	cmd := &initCommand{
		stat: func(name string) (os.FileInfo, error) { return nil, sentinel },
	}
	if _, _, err := cmd.loadConfig("/whatever"); !errors.Is(err, sentinel) {
		t.Fatalf("loadConfig stat error = %v, want sentinel", err)
	}
}
