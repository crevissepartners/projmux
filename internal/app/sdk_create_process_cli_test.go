package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
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
				if runtime.GOOS == "linux" {
					payload, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/4", owner.PID))
					if err != nil || !strings.HasPrefix(payload, filepath.Join(paths.StateDir, "pmx-detach-")) || !strings.HasSuffix(payload, " (deleted)") {
						t.Fatalf("private payload escaped frozen StateDir: %q %v", payload, err)
					}
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
				if _, err := os.Stat(filepath.Join(f.root, domain, "deep", "projmux")); !os.IsNotExist(err) {
					t.Fatalf("explicit paths wrote a sibling default root: %v", err)
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
