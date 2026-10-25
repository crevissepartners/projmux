package consumer_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestExternalClaudeHooksConsumer(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	c, err := projmux.NewClient(projmux.Options{HomeDir: home, ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var plan core.ClaudeHooksPlan
	plan, err = c.PreviewClaudeHooks(ctx, core.ClaudeHooksRequest{})
	if err != nil || plan.Exists || plan.Base != "" || !plan.Changed || plan.Conflict != "" {
		t.Fatalf("%+v %v", plan, err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("preview effects: %v %v", entries, err)
	}
	var refusal *projmux.Refusal
	_, err = c.ApplyClaudeHooks(ctx, core.ApplyClaudeHooksRequest{Base: "stale"})
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeClaudeSettingsChanged {
		t.Fatal(err)
	}
	var result core.ApplyClaudeHooksResult
	result, err = c.ApplyClaudeHooks(ctx, core.ApplyClaudeHooksRequest{Base: plan.Base})
	if err != nil || !result.Changed || result.Path != plan.Path {
		t.Fatalf("%+v %v", result, err)
	}
	if b, err := os.ReadFile(plan.Path); err != nil || string(b) != plan.Next {
		t.Fatalf("applied %q %v", b, err)
	}
	_ = []projmux.Code{projmux.CodeClaudeHooksConflict, projmux.CodeClaudeSettingsUnreadable}
}
