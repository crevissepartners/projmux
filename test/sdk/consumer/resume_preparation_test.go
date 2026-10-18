package consumer_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// The caller-exit lifecycle test also exercises the positive Prepare -> Validate
// -> Resume path against the same fixture CLI with only public imports.
func TestSDKPrepareAgentResumePublicConsumer(t *testing.T) {
	root := t.TempDir()
	c, err := projmux.NewClient(projmux.Options{HomeDir: root, ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var prepared core.PrepareAgentResumeResult
	prepared, err = c.PrepareAgentResume(ctx, core.PrepareAgentResumeRequest{Ref: "uid:agent", Binding: core.ProcessBinding{Generation: "observed"}, AllowHostLost: true})
	if !errors.Is(err, context.Canceled) || prepared != (core.PrepareAgentResumeResult{}) {
		t.Fatal(prepared, err)
	}
	var proof core.AgentResumePreparation
	proof, err = c.ValidateAgentResumePreparation(ctx, core.ValidateAgentResumePreparationRequest{Ref: "uid:agent", Preparation: core.AgentResumePreparation{AgentUID: "agent", HostLost: true}})
	if !errors.Is(err, context.Canceled) || proof != (core.AgentResumePreparation{}) {
		t.Fatal(proof, err)
	}
	_, err = c.PrepareAgentResume(context.Background(), core.PrepareAgentResumeRequest{})
	var refused *projmux.Refusal
	if !errors.As(err, &refused) || refused.Code != projmux.CodeSelectorInvalid {
		t.Fatal(err)
	}
	if projmux.CodeResumePreparationStale != "process-resume-preparation-stale" {
		t.Fatal("public code changed")
	}
}

// Crash only this test's exact process identities. A paused owner cannot create
// a notification lock after the lock-free observation and before SIGKILL.
func externalLoseResumeFixture(t *testing.T, ctx context.Context, root string, activation core.ProcessActivation) {
	t.Helper()
	owner, child := activation.HostProcess, activation.Child
	if externalStopIdentity(t, owner.PID) != owner || externalStopIdentity(t, child.PID) != child {
		t.Fatal("fixture identity changed")
	}
	paused := false
	defer func() {
		if paused {
			_ = syscall.Kill(owner.PID, syscall.SIGCONT)
		}
	}()
	for {
		if err := syscall.Kill(owner.PID, syscall.SIGSTOP); err != nil {
			t.Fatal(err)
		}
		paused = true
		for {
			raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", owner.PID))
			if err != nil {
				t.Fatal(err)
			}
			fields := strings.Fields(string(raw)[strings.LastIndex(string(raw), ")")+2:])
			if fields[0] == "T" || fields[0] == "t" {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(time.Millisecond):
			}
		}
		_, err := os.Lstat(filepath.Join(root, "deep/custom-records/notify.json.lock"))
		if os.IsNotExist(err) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if err = syscall.Kill(owner.PID, syscall.SIGCONT); err != nil {
			t.Fatal(err)
		}
		paused = false
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := syscall.Kill(owner.PID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	paused = false
	if err := syscall.Kill(child.PID, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		t.Fatal(err)
	}
	for _, identity := range []core.ProcessIdentity{owner, child} {
		for {
			raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", identity.PID))
			if os.IsNotExist(err) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			fields := strings.Fields(string(raw)[strings.LastIndex(string(raw), ")")+2:])
			if fields[0] == "Z" || fields[0] == "X" {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("fixture did not retire", ctx.Err())
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
}

// Refuse before constructing a client: its process context freezes these inputs.
// No fallback PATH or installed-provider negative probe is allowed in this fixture.
func externalFixtureProviderCheck(root, provider string) error {
	if provider != "claude" && provider != "codex" {
		return fmt.Errorf("unexpected fixture provider %q", provider)
	}
	want := filepath.Join(root, provider)
	found, err := exec.LookPath(provider)
	if err != nil || found != want {
		return fmt.Errorf("fixture provider lookup: got %q (%v), want %q", found, err, want)
	}
	info, err := os.Lstat(found)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return fmt.Errorf("fixture provider must be an owned executable file: %q", found)
	}
	if os.Getenv("PATH") != root || os.Getenv("CODEX_HOME") != root+"/.codex" || os.Getenv("CLAUDE_CONFIG_DIR") != root+"/.claude" {
		return fmt.Errorf("fixture lookup/auth roots differ from owned root %q", root)
	}
	return nil
}

func TestSDKResumeFixtureRejectsWrongProviderPath(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			root, decoy := t.TempDir(), t.TempDir()
			marker := filepath.Join(decoy, "spawned")
			// The decoy is owned by this test. Rejection must happen without exec.
			if err := os.WriteFile(filepath.Join(decoy, provider), []byte(fmt.Sprintf("#!/bin/sh\nprintf spawned > %q\n", marker)), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", decoy)
			t.Setenv("CODEX_HOME", root+"/.codex")
			t.Setenv("CLAUDE_CONFIG_DIR", root+"/.claude")
			if err := externalFixtureProviderCheck(root, provider); err == nil {
				t.Fatal("wrong provider path accepted before client construction")
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("decoy ran or marker check failed: %v", err)
			}
		})
	}
}

func externalAssertStubExecutions(t *testing.T, root, python string, count, currentPID int) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "stub-executions.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != count {
		t.Fatalf("stub call count=%d want=%d: %s", len(lines), count, raw)
	}
	python, err = filepath.EvalSymlinks(python)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int]bool{}
	for i, line := range lines {
		var call struct {
			PID             int    `json:"pid"`
			Script          string `json:"script"`
			Executable      string `json:"executable"`
			CodexHome       string `json:"codexHome"`
			ClaudeConfigDir string `json:"claudeConfigDir"`
		}
		if err := json.Unmarshal([]byte(line), &call); err != nil {
			t.Fatal(err)
		}
		if call.PID <= 0 || seen[call.PID] || call.Script != root+"/provider.py" || call.Executable != python || call.CodexHome != root+"/.codex" || call.ClaudeConfigDir != root+"/.claude" {
			t.Fatalf("unexpected stub execution: %+v", call)
		}
		seen[call.PID] = true
		if i == count-1 && currentPID != 0 {
			cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", currentPID))
			if err != nil || call.PID != currentPID || !strings.Contains(string(cmdline), root+"/provider.py") {
				t.Fatalf("actual provider PID/path mismatch: call=%+v child=%d argv=%q err=%v", call, currentPID, cmdline, err)
			}
		}
		t.Logf("STUB_EXEC count=%d pid=%d script=%s executable=%s", i+1, call.PID, call.Script, call.Executable)
	}
}
