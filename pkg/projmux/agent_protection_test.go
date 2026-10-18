package projmux_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestAgentProtectionPublicConsumerNoRuntime(t *testing.T) {
	trace := protectionRuntimeTrap(t)
	defer func() {
		if raw, err := os.ReadFile(trace); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unexpected SDK runtime/CLI calls: %q %v", raw, err)
		}
	}()
	state := t.TempDir()
	// Fixture setup only. Consumers mutate resources exclusively through Client.
	const fixture = `{"schemaVersion":5,"controlSessions":[{"apiVersion":"projmux.io/v1alpha1","kind":"ControlSession","metadata":{"uid":"ctl-test","name":"test"},"spec":{"session":"test"}}],"windows":[{"apiVersion":"projmux.io/v1alpha1","kind":"Window","metadata":{"uid":"win-test","name":"test","ownerRef":{"kind":"ControlSession","uid":"ctl-test"}},"spec":{"anchorPaneRef":"pane-test"}}],"panes":[{"apiVersion":"projmux.io/v1alpha1","kind":"Pane","metadata":{"uid":"pane-test","name":"shell","ownerRef":{"kind":"Window","uid":"win-test"}},"spec":{"role":"shell"}}],"agents":[{"apiVersion":"projmux.io/v1alpha1","kind":"Agent","metadata":{"uid":"agent-test","name":"reader","ownerRef":{"kind":"Window","uid":"win-test"},"labels":{"other":"kept"}},"spec":{"provider":"codex"},"status":{"phase":"Offline"}}],"nameReservations":[{"kind":"ControlSession","name":"test","uid":"ctl-test"},{"scope":"ctl-test","kind":"Window","name":"test","uid":"win-test"},{"scope":"ctl-test","kind":"Pane","name":"shell","uid":"pane-test"},{"scope":"ctl-test","kind":"Agent","name":"reader","uid":"agent-test"}]}`
	dir := filepath.Join(state, "metadata")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "registry.json")
	if err := os.WriteFile(path, []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := projmux.NewClient(projmux.Options{StateDir: state, SocketPath: filepath.Join(state, "absent.sock")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, protect := range []bool{true, false} {
		for attempt := range 2 {
			var result core.SetAgentProtectionResult
			result, err = c.SetAgentProtection(ctx, core.SetAgentProtectionRequest{Ref: "reader", Window: "uid:win-test", Protected: protect})
			if err != nil || result.Changed != (attempt == 0) {
				t.Fatalf("%+v %v", result, err)
			}
			value, present := result.Agent.Metadata.Labels["projmux.io/protected"]
			if present != protect || (protect && value != "true") || result.Agent.Metadata.Labels["other"] != "kept" {
				t.Fatal(result)
			}
			// Read actual persisted Registry without using any internal package or runtime.
			raw, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			var stored struct {
				Agents []core.Agent `json:"agents"`
			}
			if e = json.Unmarshal(raw, &stored); e != nil {
				t.Fatal(e)
			}
			persisted, present := stored.Agents[0].Metadata.Labels["projmux.io/protected"]
			if present != protect || persisted != value {
				t.Fatal("stored labels differ")
			}
			_, e = c.PreflightDelete(ctx, core.DeleteRequest{Kind: core.KindAgent, Ref: "uid:agent-test"})
			if protect {
				var refusal *projmux.Refusal
				if !errors.As(e, &refusal) || refusal.Code != projmux.CodeAgentProtected {
					t.Fatal(e)
				}
			} else if e != nil {
				t.Fatal(e)
			}
		}
	}
	_, err = c.SetAgentProtection(ctx, core.SetAgentProtectionRequest{Ref: "uid:missing"})
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeResourceNotFound {
		t.Fatal(err)
	}
}

// PATH traps make even a read-only tmux/provider/CLI subprocess observable.
func protectionRuntimeTrap(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	trace := filepath.Join(bin, "calls")
	t.Setenv("PROTECTION_TEST_TRACE", trace)
	t.Setenv("PATH", bin)
	for _, name := range []string{"tmux", "projmux", "claude", "codex", "antigravity"} {
		path := filepath.Join(bin, name)
		if err := os.WriteFile(path, []byte(`#!/bin/sh
printf 'runtime call\n' >> "$PROTECTION_TEST_TRACE"
exit 91
`), 0700); err != nil {
			t.Fatal(err)
		}
		if found, err := exec.LookPath(name); err != nil || found != path {
			t.Fatalf("trap unavailable: %s %v", found, err)
		}
	}
	return trace
}
