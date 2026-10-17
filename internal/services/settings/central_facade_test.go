package settings

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
)

func TestCentralSettingsCancelAtAdmission(t *testing.T) {
	for _, key := range []string{"statusbar.resources", "ai.defaultMode", "statusbar.visibility"} {
		t.Run(key, func(t *testing.T) {
			root := t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			svc := Service{Paths: func() (config.Paths, error) {
				cancel()
				return config.Paths{ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state")}, nil
			}}
			on, mode := "on", "shell"
			_, err := svc.Set(ctx, Update{Key: key, Resources: &on, DefaultMode: &mode, Visibility: map[string]string{"clock": "on"}})
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("cancel effects %v %v", entries, err)
			}
		})
	}
}
