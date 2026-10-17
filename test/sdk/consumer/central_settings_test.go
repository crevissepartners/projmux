package consumer_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestExternalCentralSettingsConsumer(t *testing.T) {
	root := t.TempDir()
	c, err := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	on, mode := "on", "codex"
	for _, u := range []core.ConfigUpdate{
		{Key: core.ConfigStatusbarResources, Resources: &on},
		{Key: core.ConfigAIDefaultMode, DefaultMode: &mode},
		{Key: core.ConfigStatusbarVisibility, Visibility: map[string]string{"clock": "off"}},
	} {
		v, err := c.SetConfig(context.Background(), u)
		if err != nil {
			t.Fatal(err)
		}
		var toggle *core.ConfigToggleState = v.Resources
		var leaves []core.ConfigVisibilityState = v.Visibility
		switch u.Key {
		case core.ConfigStatusbarResources:
			if toggle == nil || toggle.Effective != "on" || toggle.Source != "saved" {
				t.Fatal(v)
			}
		case core.ConfigAIDefaultMode:
			if v.DefaultMode != "codex" {
				t.Fatal(v)
			}
		default:
			if len(leaves) != 12 {
				t.Fatal(v)
			}
		}
	}
}
