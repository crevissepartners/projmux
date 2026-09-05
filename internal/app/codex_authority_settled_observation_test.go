package app

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

type settledObservationRunner struct{ output string }

func (r settledObservationRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return []byte(r.output), nil
}

func paneObservationLine(paneUID, authority, epoch, reason string) string {
	return strings.Join([]string{paneUID, authority, epoch, reason, "0", "0", "0", ""}, "\x1f")
}

// TestSettledAuthorityObservationNamesATornTripleOnlyUnderTheFence is the
// diagnostic half of C-5.
//
// The triple is published as three separate tmux writes, so a plain read lands
// between two of them often enough that reporting every inconsistent pairing as
// a fault would make the row noise. Under the fence the pairing cannot be an
// artefact of timing, which is what makes the verdict decidable: a torn triple
// there means a writer stopped fencing, and that is the defect that refused
// live Panes their turn.
func TestSettledAuthorityObservationNamesATornTripleOnlyUnderTheFence(t *testing.T) {
	for _, test := range []struct {
		name      string
		authority string
		epoch     string
		fence     string
		wantTorn  bool
	}{
		{name: "control plane with no epoch", authority: codexAuthorityControlPlane, fence: codexAuthorityFenceSettled, wantTorn: true},
		{name: "invalidating with no epoch", authority: codexAuthorityInvalidating, fence: codexAuthorityFenceSettled, wantTorn: true},
		{name: "hook carrying an epoch", authority: codexAuthorityHook, epoch: "752812-40", fence: codexAuthorityFenceSettled, wantTorn: true},
		{name: "control plane with its epoch", authority: codexAuthorityControlPlane, epoch: "752812-40", fence: codexAuthorityFenceSettled},
		{name: "hook with the epoch cleared", authority: codexAuthorityHook, fence: codexAuthorityFenceSettled},
		{
			name:      "the same inconsistent pairing caught in flight is not a verdict",
			authority: codexAuthorityControlPlane,
			fence:     codexAuthorityFenceContended,
		},
		{
			name:      "nor is it one on a pane whose writer never fenced",
			authority: codexAuthorityControlPlane,
			fence:     codexAuthorityFenceUnfenced,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := settledObservationRunner{output: paneObservationLine("pane-1", test.authority, test.epoch, string(codexObserverReasonEndpointSuspended))}
			observe := func(context.Context, string) (string, func()) { return test.fence, nil }
			diagnostic := observeSettledCodexLifecycleAuthority(context.Background(), runner, observe, "pane-1")
			if diagnostic.Fence != test.fence {
				t.Fatalf("fence = %q, want %q", diagnostic.Fence, test.fence)
			}
			if diagnostic.Torn != test.wantTorn {
				t.Fatalf("torn = %v, want %v for %s/%q under %s", diagnostic.Torn, test.wantTorn, test.authority, test.epoch, test.fence)
			}
		})
	}
}

// TestAuthorityFenceObservationNeverCreatesTheFenceItAsksAbout pins the
// read-only property that makes the unfenced verdict mean anything.
//
// A reader that created the fence file on the way to asking whether the Pane is
// fenced would answer its own question, and every subsequent read would report
// a fenced Pane whose writer still never takes one.
func TestAuthorityFenceObservationNeverCreatesTheFenceItAsksAbout(t *testing.T) {
	stateDir := t.TempDir()
	observe := defaultCodexAuthorityFenceObserver(stateDir)
	fence, release := observe(context.Background(), "pane-never-fenced")
	if release != nil {
		release()
	}
	if fence != codexAuthorityFenceUnfenced {
		t.Fatalf("fence = %q, want %q", fence, codexAuthorityFenceUnfenced)
	}
	if _, err := os.Stat(filepath.Join(stateDir, codexAuthorityFenceDir)); !os.IsNotExist(err) {
		t.Fatalf("the observation created the fence directory it asked about (stat err = %v)", err)
	}
}

// TestAuthorityFenceObservationYieldsToAWriterMidTransition pins that the
// observer excludes a writer rather than racing it, and that it gives up inside
// its budget instead of stalling the whole diagnosis behind a wedged writer.
func TestAuthorityFenceObservationYieldsToAWriterMidTransition(t *testing.T) {
	stateDir := t.TempDir()
	path, err := codexAuthorityFencePathIn(stateDir, "pane-live")
	if err != nil {
		t.Fatalf("derive fence: %v", err)
	}
	writer, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open fence: %v", err)
	}
	defer writer.Close()
	observe := defaultCodexAuthorityFenceObserver(stateDir)

	free, release := observe(context.Background(), "pane-live")
	if free != codexAuthorityFenceSettled {
		t.Fatalf("fence with no writer = %q, want %q", free, codexAuthorityFenceSettled)
	}
	release()

	if err := syscall.Flock(int(writer.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatalf("hold writer fence: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	held, heldRelease := observe(ctx, "pane-live")
	if heldRelease != nil {
		heldRelease()
	}
	if held != codexAuthorityFenceContended {
		t.Fatalf("fence under a writer = %q, want %q", held, codexAuthorityFenceContended)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("observation waited %s on a held fence, want it bounded by the caller deadline", elapsed)
	}
	if err := syscall.Flock(int(writer.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatalf("release writer fence: %v", err)
	}
	after, afterRelease := observe(context.Background(), "pane-live")
	if afterRelease != nil {
		afterRelease()
	}
	if after != codexAuthorityFenceSettled {
		t.Fatalf("fence after the writer finished = %q, want %q", after, codexAuthorityFenceSettled)
	}
}

// TestAuthorityCensusCountsHowEachSnapshotWasTaken pins that the census keeps
// the four sampling outcomes apart.
//
// Folding an unfenced Pane into the torn count would report a deployment fact
// as a correctness fault, and folding a contended read into either would report
// a transition in progress as a defect.
func TestAuthorityCensusCountsHowEachSnapshotWasTaken(t *testing.T) {
	diagnostics := map[string]codexLifecycleAuthorityDiagnostic{
		"pane-settled":   {Source: codexAuthorityControlPlane, Epoch: "1-1", Fence: codexAuthorityFenceSettled},
		"pane-torn":      {Source: codexAuthorityControlPlane, Fence: codexAuthorityFenceSettled, Torn: true},
		"pane-contended": {Source: codexAuthorityControlPlane, Fence: codexAuthorityFenceContended},
		"pane-unfenced":  {Source: codexAuthorityHook, Fence: codexAuthorityFenceUnfenced},
	}
	registry := registryWithCodexPanes(t, "pane-settled", "pane-torn", "pane-contended", "pane-unfenced")
	census := censusCodexLifecycleAuthority(registry, func(paneUID string) codexLifecycleAuthorityDiagnostic {
		return diagnostics[paneUID]
	})
	if census.Settled != 2 || census.Torn != 1 || census.Contended != 1 || census.Unfenced != 1 {
		t.Fatalf("census settled=%d torn=%d contended=%d unfenced=%d, want 2/1/1/1",
			census.Settled, census.Torn, census.Contended, census.Unfenced)
	}
}

func registryWithCodexPanes(t *testing.T, panes ...string) coremetadata.Registry {
	t.Helper()
	registry := coremetadata.Registry{}
	for index, pane := range panes {
		registry.Agents = append(registry.Agents, coremetadata.Agent{
			Metadata: coremetadata.ObjectMeta{UID: "agent-" + strconv.Itoa(index)},
			Spec:     coremetadata.AgentSpec{Provider: aiModeCodex},
			Status:   coremetadata.AgentStatus{PaneRef: pane},
		})
	}
	return registry
}
