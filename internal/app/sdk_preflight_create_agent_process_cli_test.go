package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// TestPreflightCreateAgentProcessMatchesDetachedCreate requires the
// preflight refusal of a process request whose profile chooses a provider
// the process creator cannot host to be the refusal the detached process
// creator gives the same request, Code and message, and that refusal to
// leave no Agent.
func TestPreflightCreateAgentProcessMatchesDetachedCreate(t *testing.T) {
	copiedCLI(t, "PMX_TEST_CLI")
	f := newProcessCreateCLI(t)
	c, err := projmux.NewClient(projmux.Options{HomeDir: f.root, ConfigDir: filepath.Join(f.root, "config", "projmux"), StateDir: filepath.Join(f.root, "state", "projmux"), ProjmuxExecutable: f.binary})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err = c.WriteProfile(ctx, "other", []byte("provider = \"antigravity\"\n")); err != nil {
		t.Fatal(err)
	}
	req := core.CreateAgentRequest{Project: "uid:" + f.project, Window: "uid:" + f.window, Profile: "other", Host: "process", Detach: true, Name: "refused", Payload: []string{"initial"}}
	_, preErr := c.PreflightCreateAgent(ctx, req)
	_, createErr := c.CreateAgent(ctx, req)
	var pre, created *projmux.Refusal
	if !errors.As(preErr, &pre) || !errors.As(createErr, &created) {
		t.Fatalf("untyped: preflight %T %v create %T %v", preErr, preErr, createErr, createErr)
	}
	if pre.Code != created.Code || preErr.Error() != createErr.Error() {
		t.Fatalf("preflight %s %q, detached create %s %q", pre.Code, preErr, created.Code, createErr)
	}
	reg, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Agents) != 0 {
		t.Fatalf("refused create left Agents %+v", reg.Agents)
	}
}
