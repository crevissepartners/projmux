package projmux_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	storemetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestSDKProjectLinkRules(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state")}
	var uid string
	_, err := storemetadata.NewDefaultStore(paths).Update(func(reg *metadata.Registry) error {
		p, e := (metadata.Mutator{DirExists: func(path string) (bool, error) { info, err := os.Stat(path); return err == nil && info.IsDir(), err }}).RegisterProject(reg, metadata.RegisterProjectOptions{Root: root, Name: "sample"})
		uid = p.Project.Metadata.UID
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := projmux.NewClient(projmux.Options{ConfigDir: paths.ConfigDir, StateDir: paths.StateDir})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	rules := core.ProjectLinkRules{Repo: []string{"https://github.com/example"}, Links: []core.ProjectLink{{LabelKey: "pr", Template: "{repo}/pull/{value}"}}}
	preview, err := c.WriteProjectLinkRules(ctx, "sample", rules, true)
	if err != nil || !preview.DryRun {
		t.Fatalf("preview %+v %v", preview, err)
	}
	path := filepath.Join(paths.ConfigDir, "project-links", uid+".json")
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preview wrote %v", err)
	}
	saved, err := c.WriteProjectLinkRules(ctx, "uid:"+uid, rules, false)
	if err != nil {
		t.Fatal(err)
	}
	rules.Repo[0] = "changed"
	if saved.Repo[0] != "https://github.com/example" {
		t.Fatal("result aliases input")
	}
	got, err := c.ReadProjectLinkRules(ctx, "sample")
	if err != nil || got.Links[0].Example != "https://github.com/example/pull/ABC-123" {
		t.Fatalf("read %+v %v", got, err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.DeleteProjectLinkRules(ctx, "sample", false)
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeLinkRulesConfirmationRequired || refusal.Field != "confirm" || refusal.Reason == "" || strings.Contains(err.Error(), "--yes") {
		t.Fatalf("SDK confirmation vocabulary: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatalf("unconfirmed delete changed file: %v", err)
	}
	invalid := core.ProjectLinkRules{Links: []core.ProjectLink{{LabelKey: "x", Template: "https://example.com/missing"}}}
	_, err = c.WriteProjectLinkRules(ctx, "sample", invalid, false)
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeInvalidLinkRules || refusal.Field != "links[0].template" || refusal.Reason != "must contain {value}" {
		t.Fatalf("refusal %v", err)
	}
	huge := core.ProjectLinkRules{}
	for i := range 64 {
		huge.Links = append(huge.Links, core.ProjectLink{LabelKey: fmt.Sprintf("k%d", i), Template: "https://example.com/" + strings.Repeat("x", 1900) + "/{value}"})
	}
	_, err = c.WriteProjectLinkRules(ctx, "sample", huge, true)
	if !errors.As(err, &refusal) || refusal.Field != "links" {
		t.Fatalf("size %v", err)
	}
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = c.ReadProjectLinkRules(ctx, "sample")
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeLinkRulesUnreadable {
		t.Fatalf("read %v", err)
	}
	for range 2 {
		out, err := c.DeleteProjectLinkRules(ctx, "sample", true)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(out)
		if strings.Contains(string(b), "null") {
			t.Fatalf("empty %s", b)
		}
	}
	_, err = c.ReadProjectLinkRules(ctx, "missing")
	if codeOf(t, err) != projmux.CodeResourceNotFound || strings.Contains(err.Error(), "--project") {
		t.Fatalf("missing %v", err)
	}
	_, err = c.ReadProjectLinkRules(ctx, "uid:")
	if codeOf(t, err) != projmux.CodeSelectorInvalid || strings.Contains(err.Error(), "--project") {
		t.Fatalf("SDK vocabulary %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = c.WriteProjectLinkRules(canceled, "sample", core.ProjectLinkRules{}, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel %v", err)
	}
}
