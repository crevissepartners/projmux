package projmux_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	storemetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestSDKProjectCandidatesCapturedInputsAndRefusal(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "repos")
	root := filepath.Join(repo, "sample")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	paths := config.Paths{ConfigDir: filepath.Join(home, "config"), StateDir: filepath.Join(home, "state")}
	t.Setenv("PROJMUX_PROJDIR", repo)
	t.Setenv("PROJMUX_MANAGED_ROOTS", "")
	t.Setenv("TMUX_SESSIONIZER_ROOTS", "")
	c, err := projmux.NewClient(projmux.Options{HomeDir: home, ConfigDir: paths.ConfigDir, StateDir: paths.StateDir})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROJMUX_PROJDIR", filepath.Join(home, "changed"))
	p, err := c.CreateProject(context.Background(), core.CreateProjectRequest{Root: root, Name: "sample"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.ListProjectCandidates(context.Background())
	if err != nil || len(got.Candidates) != 1 || got.Candidates[0].Path != root || got.Candidates[0].ProjectUID != p.Project.Metadata.UID || got.Candidates[0].Pinned {
		t.Fatalf("%+v %v", got, err)
	}
	got.Candidates[0].Path = "changed"
	again, err := c.ListProjectCandidates(context.Background())
	if err != nil || again.Candidates[0].Path != root {
		t.Fatalf("%+v %v", again, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = c.ListProjectCandidates(ctx)
	var refused *projmux.Refusal
	if !errors.Is(err, context.Canceled) || errors.As(err, &refused) {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(storemetadata.NewDefaultStore(paths).Path()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storemetadata.NewDefaultStore(paths).Path(), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = c.ListProjectCandidates(context.Background())
	if !errors.As(err, &refused) || refused.Code != projmux.CodeRegistryUnavailable {
		t.Fatalf("typed refusal: %v", err)
	}
}
