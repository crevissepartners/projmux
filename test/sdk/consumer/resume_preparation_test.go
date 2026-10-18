package consumer_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

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
