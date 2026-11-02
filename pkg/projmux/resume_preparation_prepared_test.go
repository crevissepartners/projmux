package projmux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func refusalCode(t *testing.T, err error) Code {
	t.Helper()
	var refusal *Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("not a refusal: %v", err)
	}
	return refusal.Code
}

// The handle rules are those of ResolveResourceGraphFromPrepared, and the
// entry cancellation check runs before them.
func TestValidateFromPreparedHandleRules(t *testing.T) {
	c, _ := graphFixtureClient(t, graphFixtureRegistry(t))
	prepared, _ := mustPrepare(t, c, nil)
	other, err := NewClient(Options{ConfigDir: filepath.Join(t.TempDir(), "config"), StateDir: filepath.Join(t.TempDir(), "state")})
	if err != nil {
		t.Fatal(err)
	}
	req := core.ValidateAgentResumePreparationRequest{Ref: "uid:agent-01"}
	for name, tc := range map[string]struct {
		client   *Client
		prepared *PreparedRegistrySnapshot
	}{
		"nil": {c, nil}, "zero": {c, &PreparedRegistrySnapshot{}}, "foreign root": {other, prepared},
	} {
		if _, err := tc.client.ValidateAgentResumePreparationFromPrepared(context.Background(), tc.prepared, req); refusalCode(t, err) != CodePreparedSnapshotInvalid {
			t.Fatalf("%s handle: %v", name, err)
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.ValidateAgentResumePreparationFromPrepared(canceled, nil, req); !errors.Is(err, context.Canceled) {
		t.Fatalf("entry cancellation: %v", err)
	}
	sentinel := errors.New("captured paths")
	broken := *c
	broken.pathsErr = sentinel
	if _, err := broken.ValidateAgentResumePreparationFromPrepared(context.Background(), prepared, req); !errors.Is(err, sentinel) {
		t.Fatalf("paths error: %v", err)
	}
}

// Request refusals match the other entry's codes and are judged from the
// handle: the Registry file is gone and nothing is re-read.
func TestValidateFromPreparedJudgesTheHandleNotTheFile(t *testing.T) {
	c, paths := graphFixtureClient(t, graphFixtureRegistry(t))
	prepared, _ := mustPrepare(t, c, nil)
	if err := os.Remove(store.PathFor(paths.StateDir)); err != nil {
		t.Fatal(err)
	}
	counts := countSnapshotReads(t, nil)
	ctx := context.Background()
	for name, tc := range map[string]struct {
		req  core.ValidateAgentResumePreparationRequest
		want Code
	}{
		"empty ref":     {core.ValidateAgentResumePreparationRequest{}, CodeSelectorInvalid},
		"unknown agent": {core.ValidateAgentResumePreparationRequest{Ref: "uid:agent-missing"}, CodeResourceNotFound},
		"not process":   {core.ValidateAgentResumePreparationRequest{Ref: "uid:agent-01"}, CodeResumeRequestInvalid},
	} {
		_, err := c.ValidateAgentResumePreparationFromPrepared(ctx, prepared, tc.req)
		if got := refusalCode(t, err); got != tc.want {
			t.Fatalf("%s: %s want %s (%v)", name, got, tc.want, err)
		}
	}
	if counts.loads != 0 || counts.stats != 0 {
		t.Fatalf("Registry was read: %+v", counts)
	}
}
