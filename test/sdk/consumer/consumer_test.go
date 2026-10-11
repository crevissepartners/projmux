package consumer_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
)

func TestExternalInstructionsConsumer(t *testing.T) {
	c, err := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(t.TempDir(), "config")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var written projmux.InstructionWriteResult
	written, err = c.WriteInstructions(ctx, "reviewer", []byte("exact\r\nbytes"))
	if err != nil {
		t.Fatal(err)
	}
	var list []projmux.InstructionInfo
	list, err = c.ListInstructions(ctx)
	if err != nil || len(list) != 1 || list[0].Digest != written.Digest {
		t.Fatalf("list %+v %v", list, err)
	}
	var content projmux.InstructionContent
	content, err = c.ReadInstructions(ctx, "reviewer")
	if err != nil || string(content.Content) != "exact\r\nbytes" {
		t.Fatalf("read %+v %v", content, err)
	}
	if err = c.DeleteInstructions(ctx, "reviewer"); err != nil {
		t.Fatal(err)
	}
	_, err = c.ReadInstructions(ctx, "reviewer")
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeNotFound {
		t.Fatalf("typed refusal %T %v", err, err)
	}
}
