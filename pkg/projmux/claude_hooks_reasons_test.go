package projmux_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// Mutate at the final pre-write context check to model an external edit after
// planning without timing races, internal imports, or a production test seam.
// The check count intentionally pins that boundary; the test also verifies the
// callback ran and that Apply returned the write-time refusal.
type claudeHooksEditContext struct {
	context.Context
	checks int
	edit   func()
}

func (c *claudeHooksEditContext) Err() error {
	c.checks++
	if c.checks == 4 {
		c.edit()
	}
	return c.Context.Err()
}

func TestApplyClaudeHooksPublicChangeReasons(t *testing.T) {
	for _, reason := range []string{"since-plan", "while-writing"} {
		t.Run(reason, func(t *testing.T) {
			c, _, path := claudeHooksClient(t)
			writeClaudeHooksSettings(t, path, `{"theme":"dark"}`)
			plan, err := c.PreviewClaudeHooks(context.Background(), core.ClaudeHooksRequest{})
			if err != nil {
				t.Fatal(err)
			}
			external := `{"theme":"external"}`
			edited := false
			edit := func() {
				writeClaudeHooksSettings(t, path, external)
				edited = true
			}
			var ctx context.Context = context.Background()
			if reason == "since-plan" {
				edit()
			} else {
				ctx = &claudeHooksEditContext{Context: ctx, edit: edit}
			}
			result, err := c.ApplyClaudeHooks(ctx, core.ApplyClaudeHooksRequest{Base: plan.Base})
			var refusal *projmux.Refusal
			if !errors.As(err, &refusal) {
				t.Fatalf("apply: %v, want public refusal", err)
			}
			// A public consumer can construct and classify both cases by Code
			// and Reason alone; no knowledge of the private cause is required.
			want := projmux.Refusal{Code: projmux.CodeClaudeSettingsChanged, Reason: reason}
			if refusal.Code != want.Code || refusal.Reason != want.Reason {
				t.Errorf("classification = (%q, %q), want (%q, %q)", refusal.Code, refusal.Reason, want.Code, want.Reason)
			}
			if refusal.Field != "" || result != (core.ApplyClaudeHooksResult{}) {
				t.Errorf("changed Field/result: %q, %+v", refusal.Field, result)
			}
			message := "claude-settings-changed: Claude settings " + path + " changed after it was read; nothing was written, run the command again"
			if err.Error() != message {
				t.Errorf("Error = %q, want %q", err.Error(), message)
			}
			cause := errors.Unwrap(refusal)
			if reason == "since-plan" && cause != nil {
				t.Errorf("base mismatch acquired a cause: %v", cause)
			}
			if reason == "while-writing" && (cause == nil || cause.Error() != message) {
				t.Errorf("write refusal lost its original cause: %v", cause)
			}
			got, readErr := os.ReadFile(path)
			if !edited || readErr != nil || string(got) != external {
				t.Errorf("external edit preserved: edited=%t, content=%q, err=%v", edited, got, readErr)
			}
		})
	}
}
