package consumer_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestExternalLinkRulesOptionsConsumer(t *testing.T) {
	root := t.TempDir()
	c, err := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.CreateProject(context.Background(), core.CreateProjectRequest{Root: root, Name: "sample"})
	if err != nil {
		t.Fatal(err)
	}
	options := core.ProjectLinkRulesOptions{AllowDegradedRegistry: true}
	b, err := json.Marshal(options)
	if err != nil || string(b) != `{"allowDegradedRegistry":true}` {
		t.Fatalf("options %s %v", b, err)
	}
	b, _ = json.Marshal(core.ProjectLinkRulesOptions{})
	if string(b) != "{}" {
		t.Fatal(string(b))
	}
	rules := core.ProjectLinkRules{Repo: []string{"https://github.com/example"}, Links: []core.ProjectLink{{LabelKey: "pr", Template: "{repo}/pull/{value}"}}}
	if _, err = c.WriteProjectLinkRulesWithOptions(context.Background(), "sample", rules, false, options); err != nil {
		t.Fatal(err)
	}
	got, err := c.ReadProjectLinkRulesWithOptions(context.Background(), "sample", options)
	if err != nil || len(got.Links) != 1 {
		t.Fatalf("read %+v %v", got, err)
	}
	if _, err = c.DeleteProjectLinkRulesWithOptions(context.Background(), "sample", false, options); err == nil {
		t.Fatal("confirm accepted")
	} else {
		var refusal *projmux.Refusal
		if !errors.As(err, &refusal) || refusal.Code != projmux.CodeLinkRulesConfirmationRequired {
			t.Fatal(err)
		}
	}
	if _, err = c.DeleteProjectLinkRulesWithOptions(context.Background(), "sample", true, options); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(root, "config", "project-links", got.Project+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}
