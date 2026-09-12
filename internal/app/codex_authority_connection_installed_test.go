package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/testutil/codexinstalled"
)

// No auth, model, prompt, thread or Agent input exists in this schema.
type installedConnectionInput struct {
	Root           string            `json:"root"`
	Binary         string            `json:"binary"`
	BinarySHA256   string            `json:"binarySHA256"`
	SourceHead     string            `json:"sourceHead"`
	SourceTree     string            `json:"sourceTree"`
	Releases       []string          `json:"releases"`
	HostNamespaces map[string]string `json:"hostNamespaces"`
	Evidence       string            `json:"evidence"`
}

type installedConnectionStage struct {
	Stage    string                        `json:"stage"`
	Error    string                        `json:"error,omitempty"`
	Endpoint coremetadata.CodexEndpointRef `json:"endpoint"`
	Version  string                        `json:"version,omitempty"`
	Peer     codexappserver.PeerIdentity   `json:"peer"`
}

type installedConnectionRow struct {
	Manager codexinstalled.ManagedDaemonProof `json:"manager"`
	Stages  []installedConnectionStage        `json:"stages"`
	Stopped bool                              `json:"stopped"`
}

type installedConnectionLedger struct {
	Result           string                        `json:"result"`
	SourceHead       string                        `json:"sourceHead"`
	SourceTree       string                        `json:"sourceTree"`
	BinarySHA256     string                        `json:"binarySHA256"`
	TestBinarySHA256 string                        `json:"testBinarySHA256"`
	Runtime          installedRecoveryRuntime      `json:"runtime"`
	TmuxProcess      codexinstalled.OwnedProcess   `json:"tmuxProcess"`
	RouteVerified    bool                          `json:"routeVerified"`
	RegistryVerified bool                          `json:"registryVerified"`
	Agents           int                           `json:"agents"`
	Threads          int                           `json:"threads"`
	ProviderInputs   int                           `json:"providerInputs"`
	Rows             []installedConnectionRow      `json:"rows"`
	ProcessesAfter   []codexinstalled.OwnedProcess `json:"processesAfter"`
	Cleanup          bool                          `json:"cleanup"`
}

func decodeInstalledConnectionInput(raw []byte) (installedConnectionInput, error) {
	var input installedConnectionInput
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return input, errors.New("connection diagnostic requires only explicit no-auth/no-input fixture fields")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return input, errors.New("connection diagnostic input must contain exactly one object")
	}
	if !filepath.IsAbs(input.Root) || filepath.Clean(input.Root) != input.Root || !strings.HasPrefix(input.Root, "/tmp/") || !filepath.IsAbs(input.Binary) || len(input.SourceHead) != 40 || len(input.SourceTree) != 40 || len(input.BinarySHA256) != 64 || len(input.Releases) != 2 || !filepath.IsAbs(input.Evidence) || strings.HasPrefix(filepath.Clean(input.Evidence), input.Root+string(filepath.Separator)) {
		return input, errors.New("connection diagnostic requires private root, exact binary/source, two releases and external evidence")
	}
	return input, nil
}

// TestInstalledManagedCodexConnectionDiagnostic exercises the exact production
// Current/shared/owned initialize seam with zero Agent/thread/provider inputs.
// It does not Bind, read a thread, resume a thread, or run a turn.
func TestInstalledManagedCodexConnectionDiagnostic(t *testing.T) {
	inputPath := os.Getenv("PROJMUX_CODEX_CONNECTION_INPUT")
	if inputPath == "" {
		t.Skip("requires explicit private zero-input connection diagnostic")
	}
	raw, err := codexinstalled.ReadExplicitInput(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	input, err := decodeInstalledConnectionInput(raw)
	if err != nil {
		t.Fatal(err)
	}
	isolation := codexinstalled.ManagerIsolation{HostNamespaces: input.HostNamespaces}
	if _, err := isolation.Verify(); err != nil {
		t.Fatal(err)
	}
	testDigest := verifyInstalledRecoveryBinary(t, input.Binary, input.SourceHead, input.BinarySHA256)
	ledger := installedConnectionLedger{Result: "FAIL", SourceHead: input.SourceHead, SourceTree: input.SourceTree, BinarySHA256: input.BinarySHA256, TestBinarySHA256: testDigest}
	save := func() {
		t.Helper()
		raw, err := json.MarshalIndent(ledger, "", "  ")
		if err != nil {
			t.Error("connection evidence encode failed")
			return
		}
		if err := os.WriteFile(input.Evidence, append(raw, '\n'), 0o600); err != nil {
			t.Error("connection evidence save failed")
		}
	}
	defer func() {
		if t.Failed() {
			ledger.Result = "FAIL"
		}
		save()
	}()
	fixture := newInstalledRecoveryFixture(t, input.Root)
	if err := fixture.SelectManagedRelease(input.Releases[0]); err != nil {
		t.Fatal(err)
	}
	fixture.ApplyEnv(t.Setenv)
	if _, err := os.Lstat(filepath.Join(fixture.CodexHome, "auth.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("zero-input fixture unexpectedly contains auth")
	}
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Minute)
	defer stop()
	run := installedRecoveryCommand(t, ctx)
	ledger.Runtime = setupInstalledRecoveryRuntime(t, fixture, input.Binary, run)
	save()
	route, err := resolveExactObjectRuntimeMutationRoute(ctx, inttmux.ExecRunner{}, os.Getenv)
	if err != nil || route.expectedSocketPath != ledger.Runtime.Socket || route.target.Value != ledger.Runtime.Name || route.authority == nil {
		t.Fatalf("zero-input exact route proof failed: %s", recoveryError(err))
	}
	processes, err := fixture.OwnedProcesses()
	if err != nil {
		t.Fatal("zero-input process proof unavailable")
	}
	for _, process := range processes {
		if strconv.Itoa(process.PID) == route.authority.ServerPID {
			ledger.TmuxProcess = process
		}
	}
	if ledger.TmuxProcess.Birth == "" {
		t.Fatal("exact tmux process birth unavailable")
	}
	ledger.RouteVerified = true
	registry, err := snapshotResourceRegistry()
	if err != nil {
		t.Fatal("zero-input Registry snapshot unavailable")
	}
	window, found := registry.Window(ledger.Runtime.Window)
	if _, ok := registry.Project(ledger.Runtime.Project); !ok || !found || window.Metadata.OwnerUID() != ledger.Runtime.Project || len(registry.Agents) != 0 {
		t.Fatal("zero-input Registry route disagrees with installed CLI")
	}
	ledger.RegistryVerified = true
	save()
	for index, release := range input.Releases {
		if index > 0 {
			if err := fixture.SelectManagedRelease(release); err != nil {
				t.Fatal(err)
			}
			fixture.ApplyEnv(t.Setenv)
		}
		if fixture.Versions().Managed != []string{"0.151.0", "0.154.0"}[index] {
			t.Fatal("explicit releases must be 0.151.0 then 0.154.0")
		}
		daemon, err := fixture.StartManagedRecovery(ctx, isolation)
		if err != nil {
			t.Fatal(err)
		}
		row := installedConnectionRow{Manager: daemon.Proof}
		ledger.Rows = append(ledger.Rows, row)
		save()
		record := func(stage installedConnectionStage) {
			ledger.Rows[index].Stages = append(ledger.Rows[index].Stages, stage)
			save()
		}
		probeCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		probeErr := probeInstalledRecoveryConnections(probeCtx, daemon.Proof.Version, (defaultCodexNativeThreadController{}).Current, func(route codexNativeEndpointRoute) (codexbroker.Opener, codexbroker.LifecycleOpener, error) {
			return route.brokerRoute().openers()
		}, record)
		cancel()
		// Even a failed connection diagnostic stops only this still-verified owner.
		// A failed ownership proof never falls through to generic cleanup.
		if err := daemon.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		ledger.Rows[index].Stopped = true
		save()
		if probeErr != nil {
			t.Fatalf("zero-input production connection failed: %s", recoveryError(probeErr))
		}
	}
	registry, err = snapshotResourceRegistry()
	if err != nil || len(registry.Agents) != 0 {
		t.Fatal("zero-input diagnostic created an Agent")
	}
	if got := run("tmux", "-L", ledger.Runtime.Name, "display-message", "-p", "-F", "#{socket_path}"); got != ledger.Runtime.Socket {
		t.Fatal("zero-input tmux cleanup socket changed")
	}
	run("tmux", "-S", ledger.Runtime.Socket, "kill-server")
	deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		ledger.ProcessesAfter, err = fixture.OwnedProcesses()
		if err != nil {
			t.Fatal("zero-input cleanup process evidence unavailable")
		}
		if len(ledger.ProcessesAfter) == 0 {
			break
		}
		select {
		case <-deadline.Done():
			t.Fatal("zero-input owned processes remain")
		case <-time.After(100 * time.Millisecond):
		}
	}
	if err := fixture.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(input.Root); err != nil {
		t.Fatal(err)
	}
	ledger.Cleanup = true
	ledger.Result = "PASS"
}

func probeInstalledRecoveryConnections(ctx context.Context, expectedVersion string, current func(context.Context) (codexNativeEndpointRoute, error), openers func(codexNativeEndpointRoute) (codexbroker.Opener, codexbroker.LifecycleOpener, error), record func(installedConnectionStage)) error {
	route, err := current(ctx)
	record(installedConnectionStage{Stage: "current", Endpoint: route.Endpoint, Error: recoveryError(err)})
	if err != nil {
		return err
	}
	if !route.valid() || !route.Default || route.Endpoint.EndpointGenerationID != "codex-"+expectedVersion {
		record(installedConnectionStage{Stage: "current-witness", Error: "generation-mismatch"})
		return errors.New("current provenance mismatch")
	}
	shared, owned, err := openers(route)
	if err != nil {
		record(installedConnectionStage{Stage: "openers", Error: recoveryError(err)})
		return err
	}
	endpoint, err := shared(ctx)
	record(installedConnectionStage{Stage: "shared-initialize", Endpoint: route.Endpoint, Error: recoveryError(err)})
	if err != nil {
		return err
	}
	// The narrow interface cannot invoke a provider operation at this boundary.
	witness, ok := endpoint.(interface {
		PeerIdentity() codexappserver.PeerIdentity
		NegotiatedVersion() string
	})
	if !ok || !witness.PeerIdentity().Valid() || witness.NegotiatedVersion() != expectedVersion {
		record(installedConnectionStage{Stage: "shared-witness", Error: "peer-or-version-mismatch"})
		closeErr := endpoint.Close()
		record(installedConnectionStage{Stage: "shared-close", Error: recoveryError(closeErr)})
		return errors.New("shared peer/version witness unavailable")
	}
	peer := witness.PeerIdentity()
	record(installedConnectionStage{Stage: "shared-witness", Endpoint: route.Endpoint, Version: witness.NegotiatedVersion(), Peer: peer})
	lifecycle, err := owned(ctx, peer)
	record(installedConnectionStage{Stage: "owned-initialize", Endpoint: route.Endpoint, Peer: peer, Error: recoveryError(err)})
	if err == nil {
		if !codexappserver.SamePeerIdentity(peer, lifecycle.PeerIdentity()) {
			err = codexappserver.ErrEndpointChanged
		}
		record(installedConnectionStage{Stage: "owned-witness", Peer: lifecycle.PeerIdentity(), Error: recoveryError(err)})
		closeErr := lifecycle.Close()
		record(installedConnectionStage{Stage: "owned-close", Error: recoveryError(closeErr)})
		err = errors.Join(err, closeErr)
	}
	closeErr := endpoint.Close()
	record(installedConnectionStage{Stage: "shared-close", Error: recoveryError(closeErr)})
	return errors.Join(err, closeErr)
}
