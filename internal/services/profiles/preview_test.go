package profiles

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
)

func TestParseNeverResolvesPaths(t *testing.T) {
	service := Service{Paths: func() (config.Paths, error) { t.Fatal("parse resolved storage"); return config.Paths{}, nil }, RegistryPaths: func() (config.Paths, error) { t.Fatal("parse resolved Registry"); return config.Paths{}, nil }}
	if _, err := service.Parse(context.Background(), []byte("instructions=\"missing\"")); err != nil {
		t.Fatal(err)
	}
}

func TestPreviewCancellationDuringPathResolution(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "absent")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := Service{Paths: func() (config.Paths, error) { cancel(); return config.Paths{ConfigDir: dir}, nil }, RegistryPaths: func() (config.Paths, error) { t.Fatal("preview resolved Registry"); return config.Paths{}, nil }}
	if _, err := service.Preview(ctx, "candidate", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("created files %v %v", entries, err)
	}
}
