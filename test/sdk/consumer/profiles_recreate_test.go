package consumer_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestProfileRecreatePublicConsumer(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "config")
	c, err := projmux.NewClient(projmux.Options{ConfigDir: dir, StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	original := []byte("# hand-edited original\ninstructions=\"missing\"\n")
	path := filepath.Join(dir, "profiles", "candidate.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	var result core.ProfileWriteResult
	result, err = c.RecreateProfile(ctx, "candidate", []byte("project=\"proj-aaaaaaaaaaaaaaaaaaaaaaaaaa\"\ninstructions=\"missing\""), true)
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeProfileInstructionsNotFound || result != (core.ProfileWriteResult{}) {
		t.Fatalf("result %+v, %v", result, err)
	}
	got, err := c.ReadProfile(ctx, "candidate")
	if err != nil || !bytes.Equal(got.Content, original) {
		t.Fatalf("original lost %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode %v %v", info, err)
	}
	next := []byte("project=\"proj-aaaaaaaaaaaaaaaaaaaaaaaaaa\"\nmodel=\"opus\"\n")
	if _, err = c.RecreateProfile(ctx, "candidate", next, false); !errors.As(err, &refusal) || refusal.Code != projmux.CodeProfileConfirmationRequired {
		t.Fatalf("confirmation %v", err)
	}
	result, err = c.RecreateProfile(ctx, "candidate", next, true)
	if err != nil {
		t.Fatal(err)
	}
	got, err = c.ReadProfile(ctx, "candidate")
	if err != nil || !bytes.Equal(got.Content, next) || got.Digest != result.Digest {
		t.Fatalf("replacement %+v %v", got, err)
	}
}
