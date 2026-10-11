package projmux

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestCreateProjectRefusalPreservesQuotedRoot(t *testing.T) {
	client, err := NewClient(Options{StateDir: filepath.Join(t.TempDir(), "state")})
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{
		filepath.Join(t.TempDir(), "create project requires --root caller-text"),
		"create project: --root caller-text",
	} {
		_, err = client.CreateProject(context.Background(), core.CreateProjectRequest{Root: root})
		var refusal *Refusal
		if !errors.As(err, &refusal) || refusal.Code != CodeProjectRootInvalid || !strings.Contains(err.Error(), root) {
			t.Fatalf("caller root changed: %q: %v", root, err)
		}
	}
}
