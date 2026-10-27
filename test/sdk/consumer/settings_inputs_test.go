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

func TestExternalSettingsInputsAndLinkRulesByUID(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	c, err := projmux.NewClient(projmux.Options{ConfigDir: configDir, StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	snapshot := core.RegistrySnapshot{Agents: []core.Agent{{Metadata: core.ObjectMeta{UID: "agent-plain"}}}}
	var read core.ReadAgentSettingsResult
	read, err = c.ReadAgentDisplaySettingsFromSnapshot(ctx, snapshot, core.ReadAgentSettingsRequest{AgentUIDs: []string{"agent-absent", "agent-plain"}})
	if err != nil || len(read.Agents) != 2 || read.Agents[0].AgentUID != "agent-plain" || !read.Agents[0].Cacheable || read.Agents[1].Unavailable != string(projmux.CodeResourceNotFound) || read.Agents[1].Cacheable {
		t.Fatalf("%+v %v", read, err)
	}

	const project = "proj-eeeeeeeeeeeeeeeeeeeeeeeeee"
	var paths core.SettingsInputPaths
	paths, err = projmux.SettingsInputPaths(configDir, project)
	if err != nil || paths.GuidanceFile != filepath.Join(configDir, "agent-guidance.md") || paths.ProjectLinkRulesFile != filepath.Join(configDir, "project-links", project+".json") {
		t.Fatalf("%+v %v", paths, err)
	}
	var refusal *projmux.Refusal
	if _, err = projmux.SettingsInputPaths(configDir, "uid:"+project); !errors.As(err, &refusal) || refusal.Code != projmux.CodeSelectorInvalid {
		t.Fatal(err)
	}

	var rules core.ProjectLinkRulesRead
	rules, err = c.ReadProjectLinkRulesByUID(ctx, project)
	if err != nil || rules.FileIdentity != nil || rules.Rules.Links != nil {
		t.Fatalf("%+v %v", rules, err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.ProjectLinkRulesFile), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ProjectLinkRulesFile, []byte(`{"links":[{"labelKey":"pr","template":"https://example.com/{value}"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	rules, err = c.ReadProjectLinkRulesByUID(ctx, project)
	if err != nil || rules.FileIdentity == nil || len(rules.Rules.Links) != 1 {
		t.Fatalf("%+v %v", rules, err)
	}
	var identity core.FileIdentity = *rules.FileIdentity
	_ = identity.ChangeTimeNanoseconds
	if err := os.WriteFile(paths.ProjectLinkRulesFile, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = c.ReadProjectLinkRulesByUID(ctx, project); !errors.As(err, &refusal) || refusal.Code != projmux.CodeLinkRulesUnreadable {
		t.Fatal(err)
	}
}
