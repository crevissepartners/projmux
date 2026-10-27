package projmux_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// TestWindowIntentPublicPreflight is what a consumer of the public package
// sees with no tmux server: shape refusals before any effect, the Agent
// validator's refusals with CreateAgent's Code and words, and the provider a
// profile chooses.
func TestWindowIntentPublicPreflight(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	c, err := projmux.NewClient(projmux.Options{StateDir: t.TempDir(), ConfigDir: t.TempDir(), HomeDir: t.TempDir(), ProjmuxExecutable: executable, SocketPath: filepath.Join(t.TempDir(), "absent")})
	if err != nil {
		t.Fatal(err)
	}
	project, err := c.CreateProject(ctx, core.CreateProjectRequest{Root: t.TempDir(), Name: "sample"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.WriteProfile(ctx, "coder", []byte("provider = \"codex\"\n")); err != nil {
		t.Fatal(err)
	}
	ref := "uid:" + project.Project.Metadata.UID

	preview, err := c.PreflightWindowIntent(ctx, core.WindowCreateIntent{Project: ref, Agent: &core.WindowIntentAgent{Profile: "coder"}})
	if err != nil || preview.Provider != "codex" || preview.Profile != "coder" || preview.Host != "tmux" || preview.Shell {
		t.Fatalf("profile preview %+v %v", preview, err)
	}
	if preview, err = c.PreflightWindowIntent(ctx, core.WindowCreateIntent{Project: ref, Host: "process", Detach: true, RequireEnabledProvider: true, Agent: &core.WindowIntentAgent{Provider: "claude"}}); err != nil || preview.Host != "process" {
		t.Fatalf("process preview %+v %v", preview, err)
	}

	// A shell intent resolves its Project in preflight, as an Agent intent
	// does; TestWindowIntentCLIParity holds it to CreateWindow's refusal.
	for _, agent := range []*core.WindowIntentAgent{nil, {Provider: "claude"}} {
		var refusal *projmux.Refusal
		if _, err := c.PreflightWindowIntent(ctx, core.WindowCreateIntent{Project: "uid:proj-absent", Agent: agent}); !errors.As(err, &refusal) || refusal.Code != projmux.CodeResourceNotFound {
			t.Fatalf("preflight absent Project (agent %v): %v", agent != nil, err)
		}
	}
	if preview, err := c.PreflightWindowIntent(ctx, core.WindowCreateIntent{Project: ref}); err != nil || !preview.Shell {
		t.Fatalf("shell preview %+v %v", preview, err)
	}

	for name, tc := range map[string]struct {
		intent core.WindowCreateIntent
		code   projmux.Code
	}{
		"no-project":       {core.WindowCreateIntent{}, projmux.CodeSelectorInvalid},
		"bad-window-name":  {core.WindowCreateIntent{Project: ref, Name: "bad/name"}, projmux.CodeResourceNameInvalid},
		"bad-host":         {core.WindowCreateIntent{Project: ref, Host: "ssh"}, projmux.CodeCreateRequestInvalid},
		"process-shell":    {core.WindowCreateIntent{Project: ref, Host: "process", Detach: true}, projmux.CodeCreateRequestInvalid},
		"process-attached": {core.WindowCreateIntent{Project: ref, Host: "process", Agent: &core.WindowIntentAgent{Provider: "claude"}}, projmux.CodeCreateRequestInvalid},
		"tmux-detached":    {core.WindowCreateIntent{Project: ref, Detach: true, Agent: &core.WindowIntentAgent{Provider: "claude"}}, projmux.CodeCreateRequestInvalid},
		"resume-reserved":  {core.WindowCreateIntent{Project: ref, Agent: &core.WindowIntentAgent{Provider: "claude", Resume: &core.ResumeSelection{Provider: "claude", Conversation: "c", Source: "s"}}}, projmux.CodeCreateRequestInvalid},
		"pane-not-exact":   {core.WindowCreateIntent{Project: ref, Agent: &core.WindowIntentAgent{Provider: "claude", CwdFrom: "pane", CwdPane: "shell"}}, projmux.CodeCreateRequestInvalid},
		"profile-mismatch": {core.WindowCreateIntent{Project: ref, Agent: &core.WindowIntentAgent{Provider: "claude", Profile: "coder"}}, projmux.CodeProfileProviderMismatch},
		"creator-shape":    {core.WindowCreateIntent{Project: ref, Agent: &core.WindowIntentAgent{Provider: "claude", Creator: "agent-x"}}, projmux.CodeCreateCreatorInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			for _, call := range []func() error{
				func() error { _, err := c.PreflightWindowIntent(ctx, tc.intent); return err },
				func() error { _, err := c.CreateWindowFromIntent(ctx, tc.intent); return err },
			} {
				var refusal *projmux.Refusal
				if err := call(); !errors.As(err, &refusal) || refusal.Code != tc.code {
					t.Fatalf("got %v, want %s", err, tc.code)
				}
			}
		})
	}
}
