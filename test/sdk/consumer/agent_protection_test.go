package consumer_test

import (
	"context"
	"errors"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestExternalAgentProtection(t *testing.T) {
	c, err := projmux.NewClient(projmux.Options{StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var result core.SetAgentProtectionResult
	result, err = c.SetAgentProtection(context.Background(), core.SetAgentProtectionRequest{Ref: "uid:missing", Protected: true})
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeResourceNotFound || result.Changed {
		t.Fatalf("%+v %v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = c.SetAgentProtection(ctx, core.SetAgentProtectionRequest{Ref: "uid:missing"})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
