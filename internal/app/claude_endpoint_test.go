package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	claudeadapter "github.com/crevissepartners/projmux/internal/integrations/agents/claude"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

type claudeEndpointTestFixture struct {
	bootstrap claudeEndpointBootstrap
	store     *intmetadata.Store
	provider  *exec.Cmd
	inbox     *net.UnixListener
	root      string
}

func TestClaudeEndpointHookMigrationPreservesStatusAndUserHooks(t *testing.T) {
	root := t.TempDir()
	command := testAICommand(root)
	command.readFile = os.ReadFile
	path := filepath.Join(root, claudeSettingsRelativePath)
	settings := map[string]any{"hooks": map[string]any{"SessionStart": []any{
		claudeHookManagedEntry(),
		map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "echo user-session-start"}}},
	}}}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	writeCodexTestFile(t, path, string(data))
	if err := command.Run([]string{"integrate", "claude", "--dry-run"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, data) {
		t.Fatal("dry-run changed existing hook settings")
	}
	if err := command.Run([]string{"integrate", "claude"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	installed := readClaudeSettingsTestFile(t, path)
	hooks := installed["hooks"].(map[string]any)["SessionStart"].([]any)
	status, registration, coordination, user := 0, 0, 0, 0
	for _, entry := range hooks {
		for _, raw := range entry.(map[string]any)["hooks"].([]any) {
			hook := raw.(map[string]any)
			switch hook["command"] {
			case claudeHookCommand:
				status++
			case claudeRegistrationHookCommand:
				registration++
				if hook["timeout"] != float64(5) {
					t.Fatal("registration bootstrap timeout is not bounded")
				}
			case claudeCoordinationHookCommand:
				coordination++
			case "echo user-session-start":
				user++
			}
		}
	}
	if status != 1 || registration != 1 || coordination != 1 || user != 1 {
		t.Fatal("migration changed status/user hooks or duplicated registration")
	}
	first, _ := os.ReadFile(path)
	if err := command.Run([]string{"integrate", "claude"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if second, err := os.ReadFile(path); err != nil || !bytes.Equal(first, second) {
		t.Fatal("hook migration is not idempotent")
	}
	if err := command.Run([]string{"integrate", "claude", "--remove"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	removed, _ := os.ReadFile(path)
	if bytes.Contains(removed, []byte(claudeHookManagedMarker)) || bytes.Contains(removed, []byte(claudeCoordinationManagedMarker)) || !bytes.Contains(removed, []byte("echo user-session-start")) {
		t.Fatal("removal retained managed hook or removed user hook")
	}
}

func newClaudeEndpointTestFixture(t *testing.T) *claudeEndpointTestFixture {
	t.Helper()
	root, err := os.MkdirTemp("", "pce-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	provider := exec.Command("sleep", "60")
	if err := provider.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Process.Kill(); _ = provider.Wait() })
	process, _, err := claudeadapter.Process(provider.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	h := newSessionRefHarness(t, "claude")
	if err := intmetadata.DefaultMutator().RecordClaudeProcess(h.registry, h.paneUID, h.agentUID, h.envGeneration, process); err != nil {
		t.Fatal(err)
	}
	path := intmetadata.PathFor(filepath.Join(root, "state"))
	store := intmetadata.NewStore(path)
	if _, err := store.Update(func(reg *coremetadata.Registry) error { *reg = h.registry.Clone(); return nil }); err != nil {
		t.Fatal(err)
	}
	inbox, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(root, "provider.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	inbox.SetUnlinkOnClose(false)
	t.Cleanup(func() { _ = inbox.Close() })
	if err := os.Chmod(inbox.Addr().String(), 0o600); err != nil {
		t.Fatal(err)
	}
	bootstrap, ok := claudeRegistrationBootstrap(*h.registry, path, []byte(`{"hook_event_name":"SessionStart","session_id":"actual-session"}`), func(key string) string {
		switch key {
		case internalActivationPaneUIDEnv:
			return h.paneUID
		case internalActivationGenerationEnv:
			return h.envGeneration
		case "CLAUDE_CODE_MESSAGING_SOCKET":
			return inbox.Addr().String()
		case "CLAUDE_CODE_MESSAGING_TOKEN":
			return "synthetic-private-credential-for-residue-scan"
		}
		return ""
	}, provider.Process.Pid)
	if !ok {
		t.Fatal("valid SessionStart refused")
	}
	if _, _, err := store.UpdateConvergent(func(reg *coremetadata.Registry) error {
		return intmetadata.DefaultMutator().BeginClaudeRegistration(reg, bootstrap.PaneUID, bootstrap.AgentUID, bootstrap.Generation, bootstrap.Registration.Authority)
	}); err != nil {
		t.Fatal(err)
	}
	return &claudeEndpointTestFixture{bootstrap: bootstrap, store: store, provider: provider, inbox: inbox, root: root}
}

func (f *claudeEndpointTestFixture) route(t *testing.T) (coremetadata.AgentRouteRef, string) {
	t.Helper()
	reg, err := f.store.LoadDegradedReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	return coremetadata.ResolveAgentRoute(reg, f.bootstrap.AgentUID)
}

func (f *claudeEndpointTestFixture) start(t *testing.T) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	readAck, writeAck := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- serveClaudeEndpoint(ctx, f.bootstrap, writeAck); close(done); _ = writeAck.Close() }()
	ackDone := make(chan bool, 1)
	go func() {
		var ack [1]byte
		_, err := io.ReadFull(readAck, ack[:])
		ackDone <- err == nil && ack[0] == 1
		_ = readAck.Close()
	}()
	select {
	case ok := <-ackDone:
		if !ok {
			t.Fatal("helper failed before readiness")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("helper readiness deadline")
	}
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			t.Error("helper did not exit")
		}
	})
	return cancel, done
}

func assertNoClaudeSecretResidue(t *testing.T, root string, private []string, outputs ...[]byte) {
	t.Helper()
	check := func(data []byte) {
		for _, value := range private {
			if value != "" && bytes.Contains(data, []byte(value)) {
				t.Fatal("private Claude registration residue detected")
			}
		}
	}
	for _, output := range outputs {
		check(output)
	}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			check(data)
		}
		return nil
	}); err != nil {
		t.Fatal("residue scan failed")
	}
}

func TestClaudeEndpointReadyExitInvalidated(t *testing.T) {
	t.Parallel()
	f := newClaudeEndpointTestFixture(t)
	if _, reason := f.route(t); reason == "" {
		t.Fatal("pending registration became ready")
	}
	_, done := f.start(t)
	route, reason := f.route(t)
	if reason != "" || !probeClaudeRegistrationLease(f.bootstrap.RegistryPath, route) {
		t.Fatal("exact registration not ready")
	}
	assertNoClaudeSecretResidue(t, claudeActivationLeaseDir(f.bootstrap.RegistryPath, route.PaneUID, route.Generation), []string{f.bootstrap.Token, f.bootstrap.Socket})
	before, _ := os.ReadFile(f.bootstrap.RegistryPath)
	for range 3 {
		if !probeClaudeRegistrationLease(f.bootstrap.RegistryPath, route) {
			t.Fatal("read-only lease probe failed")
		}
	}
	after, _ := os.ReadFile(f.bootstrap.RegistryPath)
	if !bytes.Equal(before, after) {
		t.Fatal("readiness probe mutated Registry")
	}
	if err := f.provider.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = f.provider.Wait()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("helper exit failed")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("helper outlived provider")
	}
	if _, reason := f.route(t); reason == "" || probeClaudeRegistrationLease(f.bootstrap.RegistryPath, route) {
		t.Fatal("exited activation remained eligible")
	}
	leasePath := claudeLeaseSocket(f.bootstrap.RegistryPath, route.PaneUID, route.Generation, f.bootstrap.Registration.Authority.RegistrationGeneration)
	if _, err := os.Lstat(filepath.Dir(leasePath)); !os.IsNotExist(err) {
		t.Fatal("helper files survived exit")
	}
	_ = f.inbox.SetDeadline(time.Now().Add(20 * time.Millisecond))
	if conn, err := f.inbox.Accept(); err == nil {
		_ = conn.Close()
		t.Fatal("provider transport connection occurred")
	}
	assertNoClaudeSecretResidue(t, f.root, []string{f.bootstrap.Token, f.bootstrap.Socket}, before, after, fmt.Appendf(nil, "%v %#v", f.bootstrap, f.bootstrap))
}

func TestClaudeEndpointRenamePreservesAndSocketReplacementInvalidates(t *testing.T) {
	t.Parallel()
	f := newClaudeEndpointTestFixture(t)
	_, done := f.start(t)
	before, reason := f.route(t)
	if reason != "" {
		t.Fatal(reason)
	}
	_, _, err := f.store.UpdateConvergent(func(reg *coremetadata.Registry) error {
		m := intmetadata.DefaultMutator()
		if _, err := m.RenameAgent(reg, f.bootstrap.AgentUID, "renamed-agent"); err != nil {
			return err
		}
		_, err := m.RenamePane(reg, f.bootstrap.PaneUID, "renamed-pane")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	after, reason := f.route(t)
	if reason != "" || !before.Same(after) || !probeClaudeRegistrationLease(f.bootstrap.RegistryPath, after) {
		t.Fatal("same-UID rename invalidated endpoint")
	}
	if err := os.Remove(f.bootstrap.Socket); err != nil {
		t.Fatal(err)
	}
	replacement, err := net.ListenUnix("unix", &net.UnixAddr{Name: f.bootstrap.Socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	replacement.SetUnlinkOnClose(false)
	defer replacement.Close()
	if err := os.Chmod(f.bootstrap.Socket, 0o600); err != nil {
		t.Fatal(err)
	}
	if probeClaudeRegistrationLease(f.bootstrap.RegistryPath, before) {
		t.Fatal("provider socket replacement remained eligible")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("socket replacement did not stop helper")
	}
	if _, err := os.Lstat(f.bootstrap.Socket); err != nil {
		t.Fatal("helper removed replacement provider socket")
	}
}

func TestClaudeCoordinationSocketReplacementInvalidatesWithoutRemovingReplacement(t *testing.T) {
	t.Parallel()
	f := newClaudeEndpointTestFixture(t)
	_, done := f.start(t)
	route, reason := f.route(t)
	if reason != "" || !probeClaudeRegistrationLease(f.bootstrap.RegistryPath, route) {
		t.Fatal("exact registration not ready")
	}
	target, ok := claudeTargetForRoute(route)
	if !ok {
		t.Fatal("exact coordination target unavailable")
	}
	path := claudeCoordinationSocket(f.bootstrap.RegistryPath, target)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	replacement, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	replacement.SetUnlinkOnClose(false)
	defer replacement.Close()
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if probeClaudeRegistrationLease(f.bootstrap.RegistryPath, route) {
		t.Fatal("replacement coordination socket remained eligible")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("coordination socket replacement did not stop helper")
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatal("helper removed replacement coordination socket")
	}
}

func TestClaudeEndpointBootstrapRejectsForeignAndSecretClaims(t *testing.T) {
	t.Parallel()
	f := newClaudeEndpointTestFixture(t)
	reg, err := f.store.LoadDegradedReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	baseEnv := map[string]string{internalActivationPaneUIDEnv: f.bootstrap.PaneUID, internalActivationGenerationEnv: f.bootstrap.Generation,
		"CLAUDE_CODE_MESSAGING_SOCKET": f.bootstrap.Socket, "CLAUDE_CODE_MESSAGING_TOKEN": f.bootstrap.Token}
	for _, test := range []struct {
		name    string
		parent  int
		session string
		change  func(*coremetadata.Registry, map[string]string)
	}{
		{"unmanaged nested producer", os.Getpid(), "actual-session", nil},
		{"secret session identity", f.provider.Process.Pid, f.bootstrap.Token, nil},
		{"stale generation", f.provider.Process.Pid, "actual-session", func(_ *coremetadata.Registry, env map[string]string) { env[internalActivationGenerationEnv] = "old" }},
		{"absent socket", f.provider.Process.Pid, "actual-session", func(_ *coremetadata.Registry, env map[string]string) {
			env["CLAUDE_CODE_MESSAGING_SOCKET"] = filepath.Join(f.root, "absent.sock")
		}},
		{"wrong owner kind", f.provider.Process.Pid, "actual-session", func(reg *coremetadata.Registry, _ map[string]string) {
			pane, _ := reg.Pane(f.bootstrap.PaneUID)
			pane.Metadata.OwnerRef.Kind = coremetadata.KindWindow
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			current := reg.Clone()
			env := map[string]string{}
			maps.Copy(env, baseEnv)
			if test.change != nil {
				test.change(&current, env)
			}
			payload, _ := json.Marshal(map[string]string{"hook_event_name": "SessionStart", "session_id": test.session})
			before := current.Clone()
			if _, ok := claudeRegistrationBootstrap(current, f.bootstrap.RegistryPath, payload, func(key string) string { return env[key] }, test.parent); ok {
				t.Fatal("foreign bootstrap accepted")
			}
			if !reflect.DeepEqual(current, before) {
				t.Fatal("refused bootstrap changed Registry")
			}
		})
	}
	if claudeHelperProducerMatches(f.bootstrap, os.Getppid()) {
		t.Fatal("unrelated helper invocation accepted")
	}
}

func TestClaudeEndpointSocketGuardsAndPrivatePeer(t *testing.T) {
	t.Parallel()
	f := newClaudeEndpointTestFixture(t)
	link := filepath.Join(f.root, "link.sock")
	if err := os.Symlink(f.bootstrap.Socket, link); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectClaudeSocket(link); err == nil {
		t.Fatal("symlink socket accepted")
	}
	if err := os.Chmod(f.bootstrap.Socket, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectClaudeSocket(f.bootstrap.Socket); err == nil {
		t.Fatal("open socket mode accepted")
	}
	if err := os.Chmod(f.bootstrap.Socket, 0o600); err != nil {
		t.Fatal(err)
	}
	// A replacement helper socket cannot borrow a different live process's
	// Registry lease merely by returning the right readiness byte.
	r := f.bootstrap.Registration
	r.Ready = true
	_, _, err := f.store.UpdateConvergent(func(reg *coremetadata.Registry) error {
		return intmetadata.DefaultMutator().RecordClaudeRegistration(reg, f.bootstrap.PaneUID, f.bootstrap.AgentUID, f.bootstrap.Generation, r)
	})
	if err != nil {
		t.Fatal(err)
	}
	route, reason := f.route(t)
	if reason != "" {
		t.Fatal(reason)
	}
	lease := claudeLeaseSocket(f.bootstrap.RegistryPath, route.PaneUID, route.Generation, r.Authority.RegistrationGeneration)
	if err := os.Mkdir(filepath.Dir(lease), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(lease)) })
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: lease, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(lease, 0o600); err != nil {
		t.Fatal(err)
	}
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			_, _ = conn.Write([]byte{1})
			_ = conn.Close()
		}
	}()
	if probeClaudeRegistrationLease(f.bootstrap.RegistryPath, route) {
		t.Fatal("replacement helper borrowed foreign process lease")
	}
}

func TestClaudeEndpointDeadLeaseWatcherInvalidatesWhileProviderLives(t *testing.T) {
	t.Parallel()
	f := newClaudeEndpointTestFixture(t)
	helper := exec.Command("sleep", "60")
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = helper.Process.Kill(); _ = helper.Wait() })
	helperIdentity, _, err := claudeadapter.Process(helper.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	f.bootstrap.Registration.Authority.LeaseProcess = helperIdentity
	_, _, err = f.store.UpdateConvergent(func(reg *coremetadata.Registry) error {
		return intmetadata.DefaultMutator().RecordClaudeRegistration(reg, f.bootstrap.PaneUID, f.bootstrap.AgentUID, f.bootstrap.Generation, f.bootstrap.Registration)
	})
	if err != nil {
		t.Fatal(err)
	}
	lease := claudeLeaseSocket(f.bootstrap.RegistryPath, f.bootstrap.PaneUID, f.bootstrap.Generation, f.bootstrap.Registration.Authority.RegistrationGeneration)
	if err := os.Mkdir(filepath.Dir(lease), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(lease)) })
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: lease, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(lease, 0o600); err != nil {
		t.Fatal(err)
	}
	target := claudeCoordinationTarget{AgentUID: f.bootstrap.AgentUID, PaneUID: f.bootstrap.PaneUID,
		Generation: f.bootstrap.Generation, Provider: aiModeClaude, Authority: f.bootstrap.Registration.Authority}
	coordinationPath := claudeCoordinationSocket(f.bootstrap.RegistryPath, target)
	coordination, err := localipc.Listen(coordinationPath)
	if err != nil {
		t.Fatal(err)
	}
	coordinationIdentity := coordination.Identity()
	if err := coordination.Unix.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(coordinationPath); err != nil {
		t.Fatal(err)
	}
	replacement, err := net.ListenUnix("unix", &net.UnixAddr{Name: coordinationPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	replacement.SetUnlinkOnClose(false)
	t.Cleanup(func() {
		_ = replacement.Close()
		_ = os.Remove(coordinationPath)
	})
	if err := os.Chmod(coordinationPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeClaudeLeaseOwner(lease+".json", f.bootstrap, coordinationIdentity); err != nil {
		t.Fatal(err)
	}
	if err := helper.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = helper.Wait()
	spec := superviseSpec{RegistryPath: f.bootstrap.RegistryPath, PaneUID: f.bootstrap.PaneUID, AgentUID: f.bootstrap.AgentUID, Generation: f.bootstrap.Generation}
	ctx := t.Context()
	go watchClaudeActivationLeases(ctx, spec)
	deadline := time.Now().Add(4 * time.Second)
	for {
		if _, reason := f.route(t); reason != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("dead helper registration was not reaped")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, _, err := claudeadapter.Process(f.provider.Process.Pid); err != nil {
		t.Fatal("provider did not remain alive")
	}
	for {
		_, leaseErr := os.Lstat(lease)
		_, receiptErr := os.Lstat(lease + ".json")
		if os.IsNotExist(leaseErr) && os.IsNotExist(receiptErr) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("dead helper files survived")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := localipc.InspectOwnedSocket(coordinationPath); err != nil {
		t.Fatalf("dead-lease reaper removed replacement coordination socket: %v", err)
	}
	assertNoClaudeSecretResidue(t, f.root, []string{f.bootstrap.Token, f.bootstrap.Socket})
}

func TestCleanupClaudeActivationLeasesPreservesCoordinationReplacement(t *testing.T) {
	t.Parallel()
	f := newClaudeEndpointTestFixture(t)
	dir := claudeActivationLeaseDir(f.bootstrap.RegistryPath, f.bootstrap.PaneUID, f.bootstrap.Generation)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	lease := claudeLeaseSocket(f.bootstrap.RegistryPath, f.bootstrap.PaneUID, f.bootstrap.Generation,
		f.bootstrap.Registration.Authority.RegistrationGeneration)
	readiness, err := net.ListenUnix("unix", &net.UnixAddr{Name: lease, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	readiness.SetUnlinkOnClose(false)
	defer readiness.Close()
	if err := os.Chmod(lease, 0o600); err != nil {
		t.Fatal(err)
	}
	target := claudeCoordinationTarget{AgentUID: f.bootstrap.AgentUID, PaneUID: f.bootstrap.PaneUID,
		Generation: f.bootstrap.Generation, Provider: aiModeClaude, Authority: f.bootstrap.Registration.Authority}
	coordinationPath := claudeCoordinationSocket(f.bootstrap.RegistryPath, target)
	coordination, err := localipc.Listen(coordinationPath)
	if err != nil {
		t.Fatal(err)
	}
	originalIdentity := coordination.Identity()
	if err := coordination.Unix.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(coordinationPath); err != nil {
		t.Fatal(err)
	}
	replacement, err := net.ListenUnix("unix", &net.UnixAddr{Name: coordinationPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	replacement.SetUnlinkOnClose(false)
	defer replacement.Close()
	if err := os.Chmod(coordinationPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeClaudeLeaseOwner(lease+".json", f.bootstrap, originalIdentity); err != nil {
		t.Fatal(err)
	}
	spec := superviseSpec{RegistryPath: f.bootstrap.RegistryPath, PaneUID: f.bootstrap.PaneUID,
		AgentUID: f.bootstrap.AgentUID, Generation: f.bootstrap.Generation}
	cleanupClaudeActivationLeases(spec)
	if _, err := localipc.InspectOwnedSocket(coordinationPath); err != nil {
		t.Fatalf("supervisor cleanup removed replacement coordination socket: %v", err)
	}
}

func TestClaudeEndpointCreatorRegistryContextIsPrivateAndExact(t *testing.T) {
	t.Parallel()
	path := testActivationRegistryPath(t)
	spec := superviseSpec{RegistryPath: path, PaneUID: "pane", Generation: "generation", ClaudeRegistration: true}
	joined := strings.Join(activationEnvironment(spec), "\n")
	if !strings.Contains(joined, internalClaudeRegistryPathEnv+"="+path) || strings.Contains(joined, "PROJMUX_") {
		t.Fatal("private creator Registry context missing or public")
	}
	spec.ClaudeRegistration = false
	if strings.Contains(strings.Join(activationEnvironment(spec), "\n"), path) {
		t.Fatal("non-Claude process inherited new private context")
	}
	spec.ClaudeRegistration = true
	spec.RuntimeID = "%7"
	stale := []string{internalActivationPaneUIDEnv + "=old-pane", internalActivationGenerationEnv + "=old-generation", internalClaudeRegistryPathEnv + "=/old/metadata/registry.json", runtimeMutationAnchorPaneEnv + "=%999"}
	actual := committedActivationEnvironment(stale, spec)
	for _, key := range []string{internalActivationPaneUIDEnv, internalActivationGenerationEnv, internalClaudeRegistryPathEnv, runtimeMutationAnchorPaneEnv} {
		count := 0
		for _, value := range actual {
			if strings.HasPrefix(value, key+"=") {
				count++
			}
		}
		if count != 1 {
			t.Fatal("private activation envelope retained duplicate authority")
		}
	}
	if strings.Contains(strings.Join(actual, "\n"), "old-") || strings.Contains(strings.Join(actual, "\n"), "%999") {
		t.Fatal("inherited activation authority survived")
	}
}

func TestClaudeEndpointOwnerReceiptFIFORefusesPromptly(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "receipt.sock.json")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan bool, 1)
	go func() { _, ok := readClaudeLeaseOwner(path, superviseSpec{}); done <- ok }()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("FIFO admitted as lease ownership")
		}
	case <-time.After(time.Second):
		t.Fatal("FIFO blocked ownership reader")
	}
}
