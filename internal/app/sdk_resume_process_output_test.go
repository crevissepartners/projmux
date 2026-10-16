package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// Compare cancellation stderr and JSON output against the preserved pre-fix
// binary. Both invocations own isolated fixture Agents and wait for exact birth.
func TestSDKDetachedProcessResumeCLICancellationOutput(t *testing.T) {
	f := deferredResumeCLIFixture(t, "claude")
	baseline := os.Getenv("PMX_TEST_RESUME_BASELINE")
	if baseline == "" {
		t.Skip("preserved pre-fix binary required for output comparison")
	}
	c, err := projmux.NewClient(projmux.Options{HomeDir: f.root, ConfigDir: f.root + "/config/projmux", StateDir: f.root + "/state/projmux", ProjmuxExecutable: f.binary})
	if err != nil {
		t.Fatal(err)
	}
	ctx, done := context.WithTimeout(context.Background(), 40*time.Second)
	defer done()
	created, err := c.CreateAgent(ctx, core.CreateAgentRequest{Project: "uid:" + f.project, Window: "uid:" + f.window, Provider: "claude", Profile: "none", Host: "process", Detach: true, Payload: []string{"initial"}})
	if err != nil {
		t.Fatal(err)
	}
	ref := "uid:" + created.Agent.Metadata.UID
	t.Cleanup(func() {
		clean, finish := context.WithTimeout(context.Background(), 15*time.Second)
		defer finish()
		if _, e := c.StopAgent(clean, core.StopAgentRequest{Ref: ref}); e != nil {
			t.Error(e)
		}
	})
	processCLIUntil(t, ctx, func() bool {
		resource, e := c.GetResource(ctx, core.Ref{Kind: core.KindAgent, Value: ref})
		if e != nil {
			t.Fatal(e)
		}
		return resource.(core.Agent).Status.SessionRef != nil
	})
	if _, err = c.StopAgent(ctx, core.StopAgentRequest{Ref: ref, Binding: created.Binding}); err != nil {
		t.Fatal(err)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(f.root, "claude"), fmt.Appendf(nil, "#!/bin/sh\nexec %q -u -c 'import sys; sys.stdin.buffer.read()'\n", python), 0700); err != nil {
		t.Fatal(err)
	}
	for _, version := range []struct {
		name, binary string
		unknown      bool
	}{{"before", baseline, true}, {"after", f.binary, false}} {
		t.Run(version.name, func(t *testing.T) {
			reg, e := f.store.LoadReadOnly()
			if e != nil {
				t.Fatal(e)
			}
			pane, _ := reg.Pane(created.Binding.PaneUID)
			old := pane.Status.ProcessSession.Binding
			cmd := exec.CommandContext(ctx, version.binary, "agent", "resume", ref, "--detach", "-o", "json", "--", "resume")
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if e = cmd.Start(); e != nil {
				t.Fatal(e)
			}
			waited := false
			defer func() {
				if !waited {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}()
			var activation m.ProcessActivation
			processCLIUntil(t, ctx, func() bool {
				reg, e := f.store.LoadReadOnly()
				if e != nil {
					t.Fatal(e)
				}
				pane, _ := reg.Pane(created.Binding.PaneUID)
				if pane.Status.Activation.Process == nil || pane.Status.ProcessSession.Binding.Generation == old.Generation {
					return false
				}
				activation = *pane.Status.Activation.Process
				return true
			})
			if e = cmd.Process.Signal(syscall.SIGTERM); e != nil {
				t.Fatal(e)
			}
			e = cmd.Wait()
			waited = true
			var exit *exec.ExitError
			if !errors.As(e, &exit) || exit.ExitCode() != 1 || stdout.Len() != 0 || strings.Contains(stderr.String(), "cleanup-unconfirmed") != version.unknown {
				t.Fatalf("CLI cancellation: exit=%v stdout=%q stderr=%q", e, &stdout, &stderr)
			}
			t.Logf("%s: exit=1 stdout=%q stderr=%q", version.name, &stdout, &stderr)
			reg, e = f.store.LoadReadOnly()
			if e != nil {
				t.Fatal(e)
			}
			pane, _ = reg.Pane(created.Binding.PaneUID)
			if !m.MatchesProcessWait(activation.Binding, pane.Status.LastTermination) || !processActivationAbsent(activation, localipc.Process) {
				t.Fatal("CLI cancellation did not retire exact child")
			}
		})
	}
}
