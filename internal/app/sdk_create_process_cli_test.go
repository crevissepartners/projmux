package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// The existing copied-binary fixture owns its HOME and Registry. This file
// intentionally adds no literal CLI-runner opt-in to its closed inventory;
// supplemental selection runs these tests against that same copied binary.
func TestSDKDetachedProcessCreateFrozenExplicitDomains(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, domain := range []string{"a", "b"} {
			t.Run(provider+"-"+domain, func(t *testing.T) {
				var f processCreateCLI
				if provider == "codex" {
					f = newProcessCodexCreateCLI(t).processCreateCLI
				} else {
					f = newProcessCreateCLI(t)
				}
				paths := config.Paths{ConfigDir: filepath.Join(f.root, domain, "deep", "custom-settings"), StateDir: filepath.Join(f.root, domain, "deep", "lifecycle-records")}
				original, err := os.ReadFile(f.store.Path())
				if err != nil {
					t.Fatal(err)
				}
				storage := intmetadata.NewStore(intmetadata.PathFor(paths.StateDir))
				reg, err := f.store.LoadReadOnly()
				if err != nil {
					t.Fatal(err)
				}
				if _, err = storage.Update(func(target *coremetadata.Registry) error { *target = reg.Clone(); return nil }); err != nil {
					t.Fatal(err)
				}
				c, err := projmux.NewClient(projmux.Options{HomeDir: f.root, ConfigDir: paths.ConfigDir, StateDir: paths.StateDir, ProjmuxExecutable: f.binary})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = c.WriteInstructions(context.Background(), "sdk-create", []byte("frozen SDK instructions")); err != nil {
					t.Fatal(err)
				}
				profile := fmt.Sprintf("provider = %q\nmodel = %q\neffort = %q\ninstructions = %q\n", provider, "stub-model", "low", "sdk-create")
				if _, err = c.WriteProfile(context.Background(), "sdk-create", []byte(profile)); err != nil {
					t.Fatal(err)
				}
				hookPath := filepath.Join(f.root, "hook-domain")
				hook := fmt.Sprintf("[hooks.post-create]\nruntime = \"process\"\nrun = %q\n", fmt.Sprintf("printf '%%s' \"$HOME\" > %q", hookPath))
				if err = os.WriteFile(filepath.Join(paths.ConfigDir, "config.toml"), []byte(hook), 0600); err != nil {
					t.Fatal(err)
				}
				sibling := filepath.Join(filepath.Dir(paths.StateDir), "projmux")
				siblingBefore := sdkCreateSeedSibling(t, sibling)
				for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "CODEX_HOME", "PATH"} {
					t.Setenv(key, filepath.Join(f.root, "poison"))
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				result, err := c.CreateAgent(ctx, core.CreateAgentRequest{Project: "uid:" + f.project, Window: "uid:" + f.window, Provider: provider, Profile: "sdk-create", Host: "process", Detach: true, Name: "frozen", Payload: []string{"initial task"}})
				cancel()
				if err != nil {
					if strings.Contains(err.Error(), "cleanup-unconfirmed") {
						_ = os.WriteFile(filepath.Join(f.root, firstInputCleanupPending), []byte("SDK startup cleanup unconfirmed"), 0600)
					}
					t.Fatalf("SDK create: %v", err)
				}
				ref := "uid:" + result.Agent.Metadata.UID
				stopAttempted := false
				defer func() {
					if !stopAttempted {
						stopAttempted = true
						clean, done := context.WithTimeout(context.Background(), 15*time.Second)
						defer done()
						if _, e := c.StopAgent(clean, core.StopAgentRequest{Ref: ref, Binding: result.Binding}); e != nil {
							_ = os.WriteFile(filepath.Join(f.root, firstInputCleanupPending), []byte("SDK Stop unconfirmed"), 0600)
							t.Errorf("cleanup %v", e)
						}
					}
				}()
				if result.Binding.AgentUID != result.Agent.Metadata.UID || result.Binding.PaneUID != result.Agent.Status.PaneRef || result.Binding.Generation == "" || len(result.Projection.Items) != 1 {
					t.Fatalf("identity %+v", result)
				}
				reg, err = storage.LoadReadOnly()
				if err != nil {
					t.Fatal(err)
				}
				agent, _ := reg.Agent(result.Agent.Metadata.UID)
				pane, _ := reg.Pane(result.Binding.PaneUID)
				if pane.Status.Activation.Process == nil {
					t.Fatal("no exact process activation")
				}
				activation := *pane.Status.Activation.Process
				owner, _, err := localipc.Process(activation.HostProcess.PID)
				if err != nil || owner != activation.HostProcess {
					t.Fatal("caller cancellation lost exact owner", err)
				}
				data, err := os.ReadFile(hookPath)
				if err != nil || string(data) != f.root {
					t.Fatalf("hook domain %q %v", data, err)
				}
				if agent.Metadata.Annotations[coremetadata.AnnotationAgentProfile] == "" {
					t.Fatal("explicit profile not applied")
				}
				if bytesAfter, e := os.ReadFile(f.store.Path()); e != nil || !bytes.Equal(original, bytesAfter) {
					t.Fatal("default Registry changed", e)
				}
				clean, done := context.WithTimeout(context.Background(), 15*time.Second)
				defer done()
				stopAttempted = true
				stop, err := c.StopAgent(clean, core.StopAgentRequest{Ref: ref, Binding: result.Binding})
				if err != nil {
					_ = os.WriteFile(filepath.Join(f.root, firstInputCleanupPending), []byte("SDK Stop unconfirmed"), 0600)
					t.Fatal(err)
				}
				if stop.Termination == nil || stop.Binding != result.Binding {
					t.Fatalf("actual Wait missing %+v", stop)
				}
				for _, identity := range []coremetadata.ProcessIdentity{activation.HostProcess, activation.Child} {
					current, _, e := localipc.Process(identity.PID)
					if !errors.Is(e, localipc.ErrProcessAbsent) && !(e == nil && current != identity) {
						t.Fatalf("not retired %+v %v", identity, e)
					}
				}
				reg, err = storage.LoadReadOnly()
				if err != nil {
					t.Fatal(err)
				}
				agent, _ = reg.Agent(result.Agent.Metadata.UID)
				pane, _ = reg.Pane(result.Binding.PaneUID)
				if agent.Status.Phase != coremetadata.PhaseOffline || !coremetadata.MatchesProcessWait(pane.Status.ProcessSession.Binding, pane.Status.LastTermination) {
					t.Fatal("Wait/UID/Pane evidence lost")
				}
				if after := sdkCreateDirectorySnapshot(t, sibling); !reflect.DeepEqual(siblingBefore, after) {
					t.Fatalf("SDK startup/Stop changed sibling Config/State: before=%v after=%v", siblingBefore, after)
				}
				deleted, err := c.Delete(clean, core.DeleteRequest{Kind: core.KindAgent, Ref: ref, Confirm: true})
				if err != nil {
					t.Fatalf("delete %+v %v", deleted, err)
				}
			})
		}
	}
}

func TestSDKDetachedProcessCreateHookRollback(t *testing.T) {
	t.Run("missing-interpreter", sdkCreateMissingInterpreter)
	f := newProcessCreateCLI(t)
	c, err := projmux.NewClient(projmux.Options{HomeDir: f.root, ConfigDir: f.root + "/config/projmux", StateDir: f.root + "/state/projmux", ProjmuxExecutable: f.binary})
	if err != nil {
		t.Fatal(err)
	}
	f.config(t, "[hooks.post-create]\nruntime = \"process\"\nrun = \"exit 7\"\n")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := c.CreateAgent(ctx, core.CreateAgentRequest{Project: "uid:" + f.project, Window: "uid:" + f.window, Provider: "claude", Profile: "none", Host: "process", Detach: true})
	var refusal *projmux.Refusal
	var detail *projmux.CreateProcessError
	if !errors.As(err, &detail) || detail.Binding != result.Binding || detail.Owner.PID == 0 {
		t.Fatalf("public typed recovery cause lost: %v", err)
	}
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeCreateExecutionFailed || !strings.Contains(err.Error(), "process-post-create-hook-failed") || errors.Unwrap(refusal) == nil || result.Binding.AgentUID == "" {
		t.Fatalf("typed rollback %+v %T %v", result, err, err)
	}
	reg, e := f.store.LoadReadOnly()
	if e != nil {
		t.Fatal(e)
	}
	if len(reg.Agents) != 0 {
		t.Fatal("rollback left Agent")
	}
}

func TestSDKDetachedProcessCreateCancellationRollsBackBirth(t *testing.T) {
	f := newProcessCreateCLI(t)
	c, err := projmux.NewClient(projmux.Options{HomeDir: f.root, ConfigDir: f.root + "/config/projmux", StateDir: f.root + "/state/projmux", ProjmuxExecutable: f.binary})
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(f.root, "hook-started")
	f.config(t, fmt.Sprintf("[hooks.post-create]\nruntime = \"process\"\nrun = %q\n", "touch "+marker+"; sleep 20"))
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	type outcome struct {
		result core.CreateAgentResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := c.CreateAgent(ctx, core.CreateAgentRequest{Project: "uid:" + f.project, Window: "uid:" + f.window, Provider: "claude", Profile: "none", Host: "process", Detach: true})
		done <- outcome{result, err}
	}()
	processCLIUntil(t, ctx, func() bool { _, err := os.Stat(marker); return err == nil })
	reg, err := f.store.LoadReadOnly()
	if err != nil || len(reg.Agents) != 1 {
		t.Fatal("birth not published", err)
	}
	pane, _ := reg.Pane(reg.Agents[0].Status.PaneRef)
	activation := *pane.Status.Activation.Process
	owner, _, err := localipc.Process(activation.HostProcess.PID)
	if err != nil || owner != activation.HostProcess || pane.Status.ProcessSession == nil || pane.Status.ProcessSession.Binding != activation.Binding {
		t.Fatal("startup barrier lost exact owner/binding", err)
	}
	if child, _, err := localipc.Process(activation.Child.PID); err != nil || child != activation.Child {
		t.Fatal("startup barrier lost exact child", err)
	}
	if runtime.GOOS == "linux" {
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, entry := range entries {
			target, err := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
			if err == nil && strings.HasPrefix(target, filepath.Join(f.root, "state/projmux", "pmx-detach-")) && strings.HasSuffix(target, " (deleted)") {
				found = true
			}
		}
		if !found {
			t.Fatal("caller startup payload escaped frozen StateDir")
		}
	}
	cancel()
	var out outcome
	select {
	case out = <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("startup cancellation did not settle")
	}
	if !errors.Is(out.err, context.Canceled) || out.result.Binding.Generation != activation.Binding.Generation {
		t.Fatalf("cause/exact generation lost %+v %v", out.result.Binding, out.err)
	}
	reg, err = f.store.LoadReadOnly()
	if err != nil || len(reg.Agents) != 0 || !processActivationAbsent(activation, localipc.Process) {
		t.Fatal("rollback/retirement not confirmed", err)
	}
}

func TestSDKProcessCreateObservationContextBoundedAndOffsetUnchanged(t *testing.T) {
	t.Run("supervisor-domain", sdkCreateSupervisorDomainCases)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	paths := config.Paths{ConfigDir: root + "/deep/custom-settings", StateDir: root + "/deep/custom-records"}
	frozen := mutationruntime.CaptureProcessCreateContext(paths, root, executable, "/bin/sh")
	caller, _, err := localipc.Process(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	request := processDetachedRequest{Options: processAgentCreateOptions{Project: selector.Ref{Kind: coremetadata.KindProject, UID: "project"}, Window: selector.Ref{Kind: coremetadata.KindWindow, UID: "window"}, Provider: "claude"}, Caller: caller, RegistryPath: intmetadata.PathFor(paths.StateDir), Context: &frozen}
	encode := func(value any) []byte {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	partial := request
	partial.Context = &mutationruntime.ProcessCreateContext{Paths: paths, HomeDir: root, Executable: executable}
	mismatch := request
	mismatch.RegistryPath = root + "/other/metadata/registry.json"
	cases := []struct {
		name     string
		data     []byte
		explicit bool
		bad      bool
	}{
		{"valid", encode(request), true, false},
		{"partial-context", encode(partial), true, true},
		{"mismatch", encode(mismatch), true, true},
		{"malformed", []byte("{"), true, true},
		{"trailing", append(encode(request), []byte("{}")...), true, true},
		{"oversized", bytes.Repeat([]byte(" "), mutationruntime.ProcessDetachedWireLimit+1), true, true},
	}
	nilContext := request
	nilContext.Context = nil
	cases = append(cases, struct {
		name     string
		data     []byte
		explicit bool
		bad      bool
	}{"cli-nil", encode(nilContext), false, false})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := os.CreateTemp(root, "payload-")
			if err != nil {
				t.Fatal(err)
			}
			defer payload.Close()
			if _, err = payload.Write(tc.data); err != nil {
				t.Fatal(err)
			}
			offset, err := payload.Seek(1, io.SeekStart)
			if err != nil {
				t.Fatal(err)
			}
			state, explicit, err := processCreateObservationStatePayload(payload)
			after, _ := payload.Seek(0, io.SeekCurrent)
			if after != offset || explicit != tc.explicit || (err != nil) != tc.bad || (!tc.bad && explicit && state != paths.StateDir) || (tc.bad && state != "") {
				t.Fatalf("state=%q explicit=%v error=%v offset=%d/%d", state, explicit, err, after, offset)
			}
		})
	}
}

// Snapshot names, modes and content, including empty lock files and directories.
func sdkCreateDirectorySnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		value := info.Mode().String()
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			value += " " + target
		} else if !entry.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += fmt.Sprintf(" %x", sha256.Sum256(data))
		}
		result[name] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func sdkCreateSeedSibling(t *testing.T, root string) map[string]string {
	t.Helper()
	for _, dir := range []string{"hooks", "logs"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range map[string]string{"config.toml": "# sibling configuration sentinel\n", "hooks/post-create": "#!/bin/sh\nexit 0\n", "logs/operations.jsonl": "{\"event\":\"sibling-sentinel\"}\n", "logs/operations.jsonl.lock": ""} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return sdkCreateDirectorySnapshot(t, root)
}
func sdkCreateMissingInterpreter(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			var f processCreateCLI
			if provider == "codex" {
				f = newProcessCodexCreateCLI(t).processCreateCLI
			} else {
				f = newProcessCreateCLI(t)
			}
			paths := config.Paths{ConfigDir: filepath.Join(f.root, "deep", "sdk-custom"), StateDir: filepath.Join(f.root, "deep", "sdk-records")}
			original, err := os.ReadFile(f.store.Path())
			if err != nil {
				t.Fatal(err)
			}
			reg, err := f.store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			store := intmetadata.NewStore(intmetadata.PathFor(paths.StateDir))
			if _, err = store.Update(func(target *coremetadata.Registry) error { *target = reg.Clone(); return nil }); err != nil {
				t.Fatal(err)
			}
			sibling := filepath.Join(filepath.Dir(paths.StateDir), "projmux")
			before := sdkCreateSeedSibling(t, sibling)
			// Lookup admits this executable file; the actual supervisor Start must fail
			// on its missing interpreter, before creating any provider process.
			if err = os.WriteFile(filepath.Join(f.root, provider), []byte("#!"+filepath.Join(f.root, "missing-interpreter")+"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			c, err := projmux.NewClient(projmux.Options{HomeDir: f.root, ConfigDir: paths.ConfigDir, StateDir: paths.StateDir, ProjmuxExecutable: f.binary})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := os.WriteFile(filepath.Join(f.root, firstInputCleanupPending), []byte("SDK missing-interpreter startup pending\n"), 0600); err != nil {
				t.Fatal(err)
			}
			_, err = c.CreateAgent(ctx, core.CreateAgentRequest{Project: "uid:" + f.project, Window: "uid:" + f.window, Provider: provider, Profile: "none", Host: "process", Detach: true})
			var detail *projmux.CreateProcessError
			if !errors.As(err, &detail) || detail.Owner.PID == 0 || errors.Unwrap(detail) == nil {
				t.Fatalf("missing interpreter lost typed owner/cause: %v", err)
			}
			after, loadErr := store.LoadReadOnly()
			if loadErr != nil || len(after.Agents) != 0 {
				t.Fatal("failed birth was not rolled back", loadErr)
			}
			if current, _, e := localipc.Process(detail.Owner.PID); !errors.Is(e, localipc.ErrProcessAbsent) && !(e == nil && current != coremetadata.ProcessIdentity(detail.Owner)) {
				t.Fatal("owner actual retirement unconfirmed", e)
			}
			if after := sdkCreateDirectorySnapshot(t, sibling); !reflect.DeepEqual(before, after) {
				t.Fatalf("failed SDK startup changed sibling: before=%v after=%v", before, after)
			}
			if data, e := os.ReadFile(f.store.Path()); e != nil || !bytes.Equal(original, data) {
				t.Fatal("default Registry changed", e)
			}
			journal := filepath.Join(paths.StateDir, "logs", "operations.jsonl")
			data, e := os.ReadFile(journal)
			if e != nil || !bytes.Contains(data, []byte("process-host-supervisor")) {
				t.Fatalf("supervisor failure journal escaped explicit State: %v %s", e, data)
			}
			if _, e = os.Stat(f.trace); !os.IsNotExist(e) {
				t.Fatal("missing interpreter produced provider input", e)
			}
			if err := os.Remove(filepath.Join(f.root, firstInputCleanupPending)); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func sdkCreateSupervisorDomainCases(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "deep", "sdk-records")
	path := intmetadata.PathFor(state)
	reg, _, paneUID := processDeleteFixture(t, "claude")
	pane, _ := reg.Pane(paneUID)
	binding := pane.Status.ProcessSession.Binding
	store := intmetadata.NewStore(path)
	if _, err := store.Update(func(target *coremetadata.Registry) error { *target = reg.Clone(); return nil }); err != nil {
		t.Fatal(err)
	}
	before := sdkCreateDirectorySnapshot(t, state)
	valid := map[string]string{internalClaudeRegistryPathEnv: path, internalActivationPaneUIDEnv: paneUID, internalActivationGenerationEnv: binding.Generation}
	cases := []struct {
		name          string
		values        map[string]string
		args          []string
		badPipe       int
		explicit, bad bool
	}{
		{"valid", valid, []string{"internal", "process-host-supervisor"}, 0, true, false},
		{"nil-cli", map[string]string{}, []string{"internal", "process-host-supervisor"}, 0, false, false},
		{"explicit-empty", map[string]string{internalClaudeRegistryPathEnv: "", internalActivationPaneUIDEnv: "", internalActivationGenerationEnv: ""}, []string{"internal", "process-host-supervisor"}, 0, true, true},
		{"partial", map[string]string{internalClaudeRegistryPathEnv: path}, []string{"internal", "process-host-supervisor"}, 0, true, true},
		{"wrong-pane", map[string]string{internalClaudeRegistryPathEnv: path, internalActivationPaneUIDEnv: "wrong", internalActivationGenerationEnv: binding.Generation}, []string{"internal", "process-host-supervisor"}, 0, true, true},
		{"stale-generation", map[string]string{internalClaudeRegistryPathEnv: path, internalActivationPaneUIDEnv: paneUID, internalActivationGenerationEnv: "replacement"}, []string{"internal", "process-host-supervisor"}, 0, true, true},
		{"noncanonical", map[string]string{internalClaudeRegistryPathEnv: state + "/../sdk-records/metadata/registry.json", internalActivationPaneUIDEnv: paneUID, internalActivationGenerationEnv: binding.Generation}, []string{"internal", "process-host-supervisor"}, 0, true, true},
		{"missing-registry", map[string]string{internalClaudeRegistryPathEnv: intmetadata.PathFor(root + "/missing"), internalActivationPaneUIDEnv: paneUID, internalActivationGenerationEnv: binding.Generation}, []string{"internal", "process-host-supervisor"}, 0, true, true},
		{"bad-fd3", valid, []string{"internal", "process-host-supervisor"}, 3, true, true},
		{"bad-fd4", valid, []string{"internal", "process-host-supervisor"}, 4, true, true},
		{"bad-fd5", valid, []string{"internal", "process-host-supervisor"}, 5, true, true},
		{"extra-argv", valid, []string{"internal", "process-host-supervisor", "unexpected"}, 0, true, true},
		{"ordinary-cli-marker-ignored", valid, []string{"get", "projects"}, 0, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := []int{}
			got, explicit, err := processCreateSupervisorDomain(tc.args, func(key string) (string, bool) { value, ok := tc.values[key]; return value, ok }, func(fd int) error {
				called = append(called, fd)
				if fd == tc.badPipe {
					return syscall.EBADF
				}
				return nil
			})
			if explicit != tc.explicit || (err != nil) != tc.bad || (tc.bad && got != "") || (!tc.bad && explicit && got != state) {
				t.Fatalf("state=%q explicit=%v err=%v", got, explicit, err)
			}
			if !tc.explicit && len(called) != 0 {
				t.Fatal("legacy/ordinary CLI consumed descriptors")
			}
			if tc.name == "valid" && !reflect.DeepEqual(called, []int{3, 4, 5}) {
				t.Fatal("descriptor fence missing", called)
			}
		})
	}
	if after := sdkCreateDirectorySnapshot(t, state); !reflect.DeepEqual(before, after) {
		t.Fatal("domain admission wrote Registry/State")
	}
	for _, tc := range []struct {
		name   string
		change func(*coremetadata.Registry)
	}{
		{"agent-pane", func(r *coremetadata.Registry) { a, _ := r.Agent(binding.AgentUID); a.Status.PaneRef = "wrong" }},
		{"pane-owner", func(r *coremetadata.Registry) { p, _ := r.Pane(binding.PaneUID); p.Metadata.OwnerRef.UID = "wrong" }},
		{"window-owner", func(r *coremetadata.Registry) { w, _ := r.Window(binding.WindowUID); w.Metadata.OwnerRef.UID = "wrong" }},
		{"session-binding", func(r *coremetadata.Registry) {
			p, _ := r.Pane(binding.PaneUID)
			p.Status.ProcessSession.Binding.WindowUID = "wrong"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := reg.Clone()
			tc.change(&bad)
			raw, err := json.Marshal(bad)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			got, explicit, err := processCreateSupervisorDomain([]string{"internal", "process-host-supervisor"}, func(key string) (string, bool) { v, ok := valid[key]; return v, ok }, func(int) error { return nil })
			if got != "" || !explicit || err == nil {
				t.Fatal("inconsistent reciprocal graph admitted", got, explicit, err)
			}
			after, e := os.ReadFile(path)
			if e != nil || !bytes.Equal(raw, after) {
				t.Fatal("invalid graph admission wrote Registry", e)
			}
		})
	}
	t.Run("pipe-content-unconsumed", func(t *testing.T) {
		read, write, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer read.Close()
		defer write.Close()
		if _, err = write.Write([]byte("spec")); err != nil {
			t.Fatal(err)
		}
		if err = processCreateSupervisorPipe(int(read.Fd())); err != nil {
			t.Fatal(err)
		}
		data := make([]byte, 4)
		if _, err = io.ReadFull(read, data); err != nil || string(data) != "spec" {
			t.Fatal("pipe consumed", err)
		}
	})
	t.Run("regular-file-offset-unchanged", func(t *testing.T) {
		file, err := os.CreateTemp(root, "regular-")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if _, err = file.Write([]byte("payload")); err != nil {
			t.Fatal(err)
		}
		offset, _ := file.Seek(2, io.SeekStart)
		if err = processCreateSupervisorPipe(int(file.Fd())); err == nil {
			t.Fatal("regular fd admitted")
		}
		after, _ := file.Seek(0, io.SeekCurrent)
		if after != offset {
			t.Fatal("regular descriptor offset changed")
		}
	})
	t.Run("command-local-env-and-nil-parity", func(t *testing.T) {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		frozen := mutationruntime.CaptureProcessCreateContext(config.Paths{ConfigDir: root + "/deep/sdk-config", StateDir: state}, root, executable, "/bin/sh")
		base := []string{"HOME=" + root, internalClaudeRegistryPathEnv + "=poison", internalActivationPaneUIDEnv + "=old", internalActivationGenerationEnv + "=old", internalClaudeRegistryPathEnv + "=duplicate"}
		saved := append([]string(nil), base...)
		c := &createCommand{processCreateContext: &frozen}
		env, err := c.processCreateSupervisorEnvironment(base, processSchemaBinding(binding), path)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(base, saved) {
			t.Fatal("provider env mutated")
		}
		values := map[string]string{}
		for _, entry := range env {
			k, v, _ := strings.Cut(entry, "=")
			if _, ok := values[k]; ok {
				t.Fatal("duplicate key", k)
			}
			values[k] = v
		}
		for k, v := range valid {
			if values[k] != v {
				t.Fatal("frozen domain not delivered", k)
			}
		}
		c.processCreateContext = nil
		legacy, err := c.processCreateSupervisorEnvironment(base, processhost.Binding{}, "")
		if err != nil || !reflect.DeepEqual(legacy, base) {
			t.Fatal("nil CLI environment changed", err)
		}
		legacy, err = c.processCreateSupervisorEnvironment(nil, processhost.Binding{}, "")
		if err != nil || legacy != nil {
			t.Fatal("nil slice broadened", err)
		}
	})
}
