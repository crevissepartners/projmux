package tmux

import (
	"context"
	"path/filepath"
	"testing"

	corelayout "github.com/crevissepartners/projmux/internal/core/layout"
	"github.com/crevissepartners/projmux/internal/core/sessionstate"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
)

func TestAuthorizeProjectLayoutBridgesExactArtifactToHookTrust(t *testing.T) {
	t.Setenv("PROJMUX_PROJECT_HOOKS", "off")

	repo := t.TempDir()
	artifact := corelayout.Artifact{
		Name:         "team",
		Path:         filepath.Join(repo, ".projmux", "layouts", "team.toml"),
		RelativePath: ".projmux/layouts/team.toml",
		Contents:     []byte("exact layout bytes"),
		Preset: corelayout.Preset{
			SchemaVersion: corelayout.SchemaVersion,
			Windows: []corelayout.Window{{
				Index:           0,
				ActivePaneIndex: 0,
				Panes: []corelayout.Pane{{
					Index:  0,
					Recipe: sessionstate.StartupRecipe("make watch"),
				}},
			}},
		},
	}
	var request hooks.ProjectHookPromptRequest
	lifecycle := &hooks.Runner{
		TrustStorePath: filepath.Join(t.TempDir(), "trusted-projects.json"),
		ProjectHookPrompt: func(req hooks.ProjectHookPromptRequest) hooks.ProjectHookDecision {
			request = req
			return hooks.ProjectHookDeny
		},
	}
	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("layout authorization must not invoke tmux")
		return nil, nil
	}), WithLifecycleHookRunner(lifecycle))

	ok, err := client.AuthorizeProjectLayout(context.Background(), repo, artifact)
	if err != nil {
		t.Fatalf("AuthorizeProjectLayout() error = %v", err)
	}
	if ok {
		t.Fatal("AuthorizeProjectLayout() = true after deny")
	}
	if request.RelativePath != artifact.RelativePath || request.ArtifactKind != "project layout" {
		t.Fatalf("request = %#v", request)
	}
}

func TestAuthorizeProjectLayoutFailsClosedWithoutAuthorizer(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("layout authorization must not invoke tmux")
		return nil, nil
	}))
	artifact := corelayout.Artifact{
		Path:         filepath.Join(repo, ".projmux", "layouts", "team.toml"),
		RelativePath: ".projmux/layouts/team.toml",
		Contents:     []byte("command"),
		Preset: corelayout.Preset{Windows: []corelayout.Window{{
			Index: 0,
			Panes: []corelayout.Pane{{
				Index:  0,
				Recipe: sessionstate.StartupRecipe("unsafe"),
			}},
		}}},
	}
	ok, err := client.AuthorizeProjectLayout(context.Background(), repo, artifact)
	if err == nil || ok {
		t.Fatalf("AuthorizeProjectLayout() = %v, %v; want fail closed", ok, err)
	}
}

func TestAuthorizeProjectLayoutCommandlessPreservesBehaviorWithoutAuthorizer(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("commandless layout authorization must not invoke tmux")
		return nil, nil
	}))
	ok, err := client.AuthorizeProjectLayout(context.Background(), t.TempDir(), corelayout.Artifact{})
	if err != nil || !ok {
		t.Fatalf("AuthorizeProjectLayout() = %v, %v; want true, nil", ok, err)
	}
}
