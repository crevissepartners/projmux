package consumer_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
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

func TestExternalResourceConsumer(t *testing.T) {
	root := t.TempDir()
	c, err := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state"), SocketPath: filepath.Join(root, "absent.sock")})
	if err != nil {
		t.Fatal(err)
	}
	if c.Version() == "" {
		t.Fatal("empty version")
	}
	for _, kind := range []core.Kind{core.KindProject, core.KindWindow, core.KindPane, core.KindAgent} {
		var list core.ResourceList
		list, err = c.ListResources(context.Background(), kind, projmux.ResourceSelector{})
		if err != nil || list.Items == nil || len(list.Items) != 0 {
			t.Fatalf("%s: %+v %v", kind, list, err)
		}
		_, err = c.GetResource(context.Background(), core.Ref{Kind: kind, Value: "uid:missing"})
		var refusal *projmux.Refusal
		if !errors.As(err, &refusal) || refusal.Code != projmux.CodeResourceNotFound {
			t.Fatalf("%s: %v", kind, err)
		}
	}
	// Concrete public fields remain usable without importing an internal package.
	_ = core.Agent{Metadata: core.ObjectMeta{UID: "agent-example"}, Status: core.AgentStatus{SessionRef: &core.AgentSessionRef{Codex: &core.CodexSessionRef{ThreadID: "thread"}}}}
}

func TestExternalRuntimeConsumer(t *testing.T) {
	root := t.TempDir()
	c, err := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []core.RuntimeObjectKind{core.RuntimeSessions, core.RuntimeWindows, core.RuntimePanes} {
		var report core.RuntimeReport
		report, err = c.ObserveRuntime(context.Background(), projmux.RuntimeRequest{Kind: kind})
		if err != nil || report.Transport.Kind != "none" || report.Items == nil || len(report.Unavailable) == 0 {
			t.Fatalf("%+v %v", report, err)
		}
	}
	_, err = c.ObserveRuntime(context.Background(), projmux.RuntimeRequest{Kind: core.RuntimeSessions, SocketPath: "relative"})
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeRuntimeRequestInvalid {
		t.Fatal(err)
	}
	_ = core.RuntimeRow{Resource: &core.RuntimeResource{UID: "pane-example"}, Conflicts: []core.RuntimeConflict{{Targets: []string{"%1"}}}}
}
