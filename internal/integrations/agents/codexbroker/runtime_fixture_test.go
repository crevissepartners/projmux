package codexbroker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// The runtime and IPC contracts are stated in terms of separate OS processes:
// one singleton, one credential, one socket, and a crash that a client has to
// survive. None of that is observable inside one process, so this fixture
// re-executes the test binary in two helper roles and drives the real wire
// between them.
const (
	helperRoleEnv     = "PROJMUX_CODEX_BROKER_TEST_ROLE"
	helperDomainEnv   = "PROJMUX_CODEX_BROKER_TEST_DOMAIN"
	helperLedgerEnv   = "PROJMUX_CODEX_BROKER_TEST_LEDGER"
	helperReportEnv   = "PROJMUX_CODEX_BROKER_TEST_REPORT"
	helperGateEnv     = "PROJMUX_CODEX_BROKER_TEST_GATE"
	helperReleaseEnv  = "PROJMUX_CODEX_BROKER_TEST_RELEASE"
	helperThreadEnv   = "PROJMUX_CODEX_BROKER_TEST_THREAD"
	helperIdleEnv     = "PROJMUX_CODEX_BROKER_TEST_IDLE"
	helperProtocolEnv = "PROJMUX_CODEX_BROKER_TEST_PROTOCOL"

	helperRoleRuntime = "runtime"
	helperRoleClient  = "client"

	helperReadyLine = "helper ready"
)

// TestMain turns this test binary into its own helper process. A role is only
// ever set by a test that spawned the process, so a normal run reaches m.Run
// untouched.
func TestMain(m *testing.M) {
	switch os.Getenv(helperRoleEnv) {
	case helperRoleRuntime:
		os.Exit(runHelperRuntime())
	case helperRoleClient:
		os.Exit(runHelperClient())
	}
	os.Exit(m.Run())
}

// ledgerEndpoint is an endpoint whose every upstream call is appended to a file
// shared with the parent process. It is how a multi-process test counts the
// connections and the writes a runtime made without being able to read that
// runtime's memory.
type ledgerEndpoint struct {
	path   string
	events chan codexappserver.Notification
	once   sync.Once
}

func newLedgerEndpoint(path string) *ledgerEndpoint {
	return &ledgerEndpoint{path: path, events: make(chan codexappserver.Notification, 64)}
}

func (e *ledgerEndpoint) Notifications() <-chan codexappserver.Notification { return e.events }

func (e *ledgerEndpoint) Request(_ context.Context, method string, _, result any) error {
	e.append("request " + method)
	if target, ok := result.(*json.RawMessage); ok {
		*target = json.RawMessage(`{"accepted":true}`)
	}
	return nil
}

func (e *ledgerEndpoint) RespondServerRequest(_ context.Context, rawID json.RawMessage, _ any) error {
	e.append("respond " + string(rawID))
	return nil
}

func (e *ledgerEndpoint) BootstrapThread(_ context.Context, threadID, cwd string, _ []string) (codexappserver.ThreadSnapshot, error) {
	e.append("snapshot " + threadID)
	return codexappserver.ThreadSnapshot{ThreadID: threadID, CWD: cwd, RuntimeStatus: "idle"}, nil
}

func (e *ledgerEndpoint) Close() error {
	e.once.Do(func() { close(e.events) })
	return nil
}

// append records one upstream call. O_APPEND writes of this size are atomic, so
// two runtimes sharing one ledger would each leave their own visible line.
func (e *ledgerEndpoint) append(line string) {
	appendLedger(e.path, line)
}

func appendLedger(path, line string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304 -- test-owned ledger path.
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.WriteString(line + "\n")
}

func readLedger(t *testing.T, path string) []string {
	t.Helper()
	payload, err := os.ReadFile(path) // #nosec G304 -- test-owned ledger path.
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read ledger: %v", err)
	}
	var lines []string
	for line := range strings.SplitSeq(string(payload), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func countLedger(t *testing.T, path, prefix string) int {
	t.Helper()
	count := 0
	for _, line := range readLedger(t, path) {
		if strings.HasPrefix(line, prefix) {
			count++
		}
	}
	return count
}

// helperProtocol reads the version window a helper process should speak. It is
// how one test binary stands in for two installed binaries: a runtime started
// with one window and a client started with another is exactly the rolling
// replacement the drain contract is about.
func helperProtocol(value string) ProtocolRange {
	parts := strings.SplitN(strings.TrimSpace(value), ":", 2)
	if len(parts) != 2 {
		return CurrentProtocol()
	}
	minimum, minErr := strconv.Atoi(parts[0])
	preferred, maxErr := strconv.Atoi(parts[1])
	if minErr != nil || maxErr != nil {
		return CurrentProtocol()
	}
	return ProtocolRange{Preferred: preferred, Minimum: minimum}
}

// runHelperRuntime hosts one broker runtime over the shared ledger endpoint.
func runHelperRuntime() int {
	ledger := os.Getenv(helperLedgerEnv)
	discovery, err := NewDiscovery(os.Getenv(helperDomainEnv), DefaultEndpointKey)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper discovery:", err)
		return 1
	}
	opened := 0
	broker, err := NewBroker(Config{Opener: func(context.Context) (Endpoint, error) {
		opened++
		appendLedger(ledger, "open "+strconv.Itoa(os.Getpid())+" "+strconv.Itoa(opened))
		return newLedgerEndpoint(ledger), nil
	}})
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper broker:", err)
		return 1
	}
	idle, _ := time.ParseDuration(os.Getenv(helperIdleEnv))
	host, err := StartHost(HostConfig{
		Discovery:   discovery,
		Broker:      broker,
		IdleTimeout: idle,
		Protocol:    helperProtocol(os.Getenv(helperProtocolEnv)),
	})
	if err != nil {
		_ = broker.Close()
		fmt.Fprintln(os.Stderr, "helper host:", err)
		return 1
	}
	appendLedger(ledger, "runtime "+strconv.Itoa(os.Getpid())+" "+host.RuntimeID())
	fmt.Println(helperReadyLine)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(signals)
	select {
	case <-host.Done():
	case <-signals:
	}
	_ = host.Close()
	appendLedger(ledger, "stopped "+strconv.Itoa(os.Getpid()))
	return 0
}

// runHelperClient reaches the runtime, binds one thread, and holds it until the
// parent releases it.
func runHelperClient() int {
	discovery, err := NewDiscovery(os.Getenv(helperDomainEnv), DefaultEndpointKey)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper discovery:", err)
		return 1
	}
	waitForFile(os.Getenv(helperGateEnv), 20*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, err := Ensure(ctx, discovery, EnsureConfig{
		Protocol: helperProtocol(os.Getenv(helperProtocolEnv)),
		Launch:   helperRuntimeLauncher(discovery),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper ensure:", RefusalOf(err))
		return 1
	}
	defer conn.Close()
	binding, err := conn.Bind(ctx, os.Getenv(helperThreadEnv), "/work/project", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper bind:", RefusalOf(err))
		return 1
	}
	select {
	case event, ok := <-binding.Events():
		if !ok || event.Origin != EventOriginSnapshot {
			fmt.Fprintln(os.Stderr, "helper snapshot:", binding.Revocation())
			return 1
		}
	case <-ctx.Done():
		fmt.Fprintln(os.Stderr, "helper snapshot: timeout")
		return 1
	}
	if err := os.WriteFile(os.Getenv(helperReportEnv),
		[]byte("runtime "+conn.Runtime()+" protocol "+strconv.Itoa(conn.Protocol())+"\n"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "helper report:", err)
		return 1
	}
	waitForFile(os.Getenv(helperReleaseEnv), 20*time.Second)
	_ = binding.Close()
	return 0
}

// helperRuntimeLauncher starts the runtime role of this same binary.
func helperRuntimeLauncher(discovery Discovery) Launcher {
	return func(context.Context) error {
		cmd := helperCommand(helperRoleRuntime, discovery.Domain(), map[string]string{
			helperLedgerEnv:   os.Getenv(helperLedgerEnv),
			helperIdleEnv:     os.Getenv(helperIdleEnv),
			helperProtocolEnv: os.Getenv(helperProtocolEnv),
		})
		if err := cmd.Start(); err != nil {
			return err
		}
		go func() { _ = cmd.Wait() }()
		return nil
	}
}

// helperCommand builds one re-execution of this test binary in a helper role.
func helperCommand(role, domain string, env map[string]string) *exec.Cmd {
	cmd := exec.Command(os.Args[0]) // #nosec G204 -- os.Args[0] is this test binary.
	cmd.Env = append(os.Environ(), helperRoleEnv+"="+role, helperDomainEnv+"="+domain)
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	return cmd
}

func waitForFile(path string, timeout time.Duration) {
	if strings.TrimSpace(path) == "" {
		return
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// newRuntimeDomain returns a short-enough private state domain.
//
// The path length matters: a Unix socket path has a platform bound, and the
// discovery contract refuses a domain that would exceed it rather than
// creating something that cannot be bound. Preferring /tmp keeps the fixture
// inside that bound on every supported target.
func newRuntimeDomain(t *testing.T) string {
	t.Helper()
	base := os.TempDir()
	if info, err := os.Stat("/tmp"); err == nil && info.IsDir() {
		base = "/tmp"
	}
	root, err := os.MkdirTemp(base, "pmxb")
	if err != nil {
		t.Fatalf("create state domain: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if _, err := NewDiscovery(root, DefaultEndpointKey); err != nil {
		t.Fatalf("state domain %q is unusable: %v", root, err)
	}
	return root
}

// newRuntimeDiscovery builds the discovery contract for a fresh state domain.
func newRuntimeDiscovery(t *testing.T) Discovery {
	t.Helper()
	discovery, err := NewDiscovery(newRuntimeDomain(t), DefaultEndpointKey)
	if err != nil {
		t.Fatalf("NewDiscovery() = %v", err)
	}
	return discovery
}

// startTestHost publishes one in-process runtime over a fake endpoint.
func startTestHost(t *testing.T, discovery Discovery, idle time.Duration, protocol ProtocolRange) (*Host, *fakeEndpoint) {
	t.Helper()
	endpoint := newFakeEndpoint()
	broker, _, _ := newTestBroker(t, 8, endpoint)
	host, err := StartHost(HostConfig{
		Discovery: discovery, Broker: broker, IdleTimeout: idle, Protocol: protocol,
	})
	if err != nil {
		t.Fatalf("StartHost() = %v", err)
	}
	t.Cleanup(func() { _ = host.Close() })
	return host, endpoint
}

// dialTestClient opens one authenticated client session.
func dialTestClient(t *testing.T, discovery Discovery, protocol ProtocolRange) *Conn {
	t.Helper()
	conn, err := Dial(t.Context(), discovery, DialConfig{Protocol: protocol})
	if err != nil {
		t.Fatalf("Dial() = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// boundRemote binds one thread over IPC and waits for the snapshot that opens
// control authority.
func boundRemote(t *testing.T, conn *Conn, threadID string) (*RemoteBinding, Fence) {
	t.Helper()
	binding, err := conn.Bind(t.Context(), threadID, "/work/project", nil)
	if err != nil {
		t.Fatalf("Bind(%s) = %v", threadID, err)
	}
	event := nextRemoteEvent(t, binding)
	if event.Origin != EventOriginSnapshot || event.Snapshot.ThreadID != threadID {
		t.Fatalf("first event = %+v, want the barrier snapshot for %s", event, threadID)
	}
	fence, err := binding.ControlAuthority()
	if err != nil {
		t.Fatalf("ControlAuthority(%s) = %v", threadID, err)
	}
	return binding, fence
}

// nextRemoteEvent reads one delivery from a remote binding.
func nextRemoteEvent(t *testing.T, binding *RemoteBinding) Event {
	t.Helper()
	select {
	case event, ok := <-binding.Events():
		if !ok {
			t.Fatalf("%s stream closed early: %s", binding.ThreadID(), binding.Revocation())
		}
		return event
	case <-time.After(10 * time.Second):
		t.Fatalf("%s delivered no event", binding.ThreadID())
		return Event{}
	}
}

// helperProcess owns one spawned helper so a test can wait on it, kill it, and
// clean it up without two owners racing to reap the same pid.
type helperProcess struct {
	cmd  *exec.Cmd
	mu   sync.Mutex
	done bool
	err  error
}

// wait reaps the helper exactly once and returns its exit result.
func (p *helperProcess) wait() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.done {
		p.err = p.cmd.Wait()
		p.done = true
	}
	return p.err
}

// kill terminates exactly the process this test spawned. It resolves the
// target from its own child handle, never by searching for a name or a socket,
// so no ambient broker, app-server, or tmux process can be reached from here.
func (p *helperProcess) kill() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done {
		return
	}
	_ = p.cmd.Process.Signal(syscall.SIGKILL)
	p.err = p.cmd.Wait()
	p.done = true
}

// startHelper spawns one helper role and registers its cleanup.
func startHelper(t *testing.T, role, domain string, env map[string]string) *helperProcess {
	t.Helper()
	cmd := helperCommand(role, domain, env)
	cmd.Stderr = os.Stderr
	helper := &helperProcess{cmd: cmd}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s helper: %v", role, err)
	}
	t.Cleanup(helper.kill)
	return helper
}

// startRuntimeProcess starts the runtime helper and waits for its readiness
// line, so a test never races the publication it is about to dial.
func startRuntimeProcess(t *testing.T, discovery Discovery, ledger string, idle time.Duration, protocol string) *helperProcess {
	t.Helper()
	cmd := helperCommand(helperRoleRuntime, discovery.Domain(), map[string]string{
		helperLedgerEnv:   ledger,
		helperIdleEnv:     idle.String(),
		helperProtocolEnv: protocol,
	})
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("runtime helper stdout: %v", err)
	}
	cmd.Stderr = os.Stderr
	helper := &helperProcess{cmd: cmd}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start runtime helper: %v", err)
	}
	t.Cleanup(helper.kill)
	line := make(chan string, 1)
	go func() {
		buffer := make([]byte, len(helperReadyLine)+1)
		if _, readErr := stdout.Read(buffer); readErr == nil {
			line <- string(buffer)
		}
		close(line)
	}()
	select {
	case value, ok := <-line:
		if !ok || !strings.Contains(value, helperReadyLine) {
			t.Fatalf("runtime helper did not report readiness: %q", value)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("runtime helper never reported readiness")
	}
	return helper
}

// waitFor polls a condition to a bounded deadline.
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// domainFiles lists every readable regular file under one state domain. A
// socket has no contents to audit; everything else the runtime leaves behind
// does.
func domainFiles(t *testing.T, domain string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(domain, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk state domain: %v", err)
	}
	return found
}
