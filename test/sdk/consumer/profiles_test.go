package consumer_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestTypedProfileParseAndPreview(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "config")
	c, err := projmux.NewClient(projmux.Options{ConfigDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	content := []byte("provider=\"codex\"\n[permissions]\nsandbox=\"read-only\"")
	var spec core.ProfileSpec
	spec, err = c.ParseProfile(ctx, content)
	if err != nil || spec.Provider != "codex" || !spec.Permissions.HasPermissions() {
		t.Fatalf("%+v %v", spec, err)
	}
	var candidate core.ProfilePreview
	candidate, err = c.PreviewProfile(ctx, "reader", content)
	if err != nil || !candidate.DryRun || candidate.Spec.Permissions.Sandbox != "read-only" || candidate.Size != int64(len(content)) {
		t.Fatalf("%+v %v", candidate, err)
	}
	b, err := json.Marshal(candidate)
	if err != nil || strings.Contains(string(b), "null") {
		t.Fatalf("%s %v", b, err)
	}
	if _, err = os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preview created storage: %v", err)
	}
	_, err = c.ParseProfile(ctx, []byte("[unknown]"))
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeProfileTableUnknown {
		t.Fatalf("%v", err)
	}
}
