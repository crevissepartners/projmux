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

func TestExternalAgentSettingsConsumer(t *testing.T) {
	root := t.TempDir()
	c, err := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	var read core.ReadAgentSettingsResult
	read, err = c.ReadAgentSettings(context.Background(), core.ReadAgentSettingsRequest{AgentUIDs: []string{"agent-absent"}})
	if err != nil || len(read.Agents) != 1 || read.Agents[0].Unavailable != string(projmux.CodeResourceNotFound) {
		t.Fatalf("%+v %v", read, err)
	}
	var refusal *projmux.Refusal
	_, err = c.ReadAgentSettings(context.Background(), core.ReadAgentSettingsRequest{AgentUIDs: []string{""}})
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeSelectorInvalid {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("first-use effects: %v %v", entries, err)
	}
	var resolution core.AgentSettingsResolution = read.Agents[0]
	_ = core.AgentLaunchSettings{Profile: core.AgentLaunchProfile{Name: "lead"}, Model: core.AgentLaunchSetting{Value: "opus", Override: true}}
	_ = resolution.Current.Effort.ProfileValue
}
