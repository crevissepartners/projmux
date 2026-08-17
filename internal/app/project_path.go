package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	coresessions "github.com/crevissepartners/projmux/internal/core/sessions"
)

type sessionIdentityResolver interface {
	SessionIdentityForPath(path string) (string, error)
}

type currentPathResolver interface {
	CurrentPanePath(ctx context.Context) (string, error)
}

type currentIdentityResolver struct {
	namer coresessions.Namer
}

func newDefaultCurrentIdentityResolver() (sessionIdentityResolver, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return currentIdentityResolver{namer: coresessions.NewNamer(homeDir)}, nil
}

func (r currentIdentityResolver) SessionIdentityForPath(path string) (string, error) {
	return r.namer.SessionName(filepath.Clean(path)), nil
}

func validateDirectory(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat current pane path: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("current pane path is not a directory: %s", path)
	}
	return nil
}
