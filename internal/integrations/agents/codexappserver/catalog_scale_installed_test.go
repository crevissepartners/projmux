package codexappserver_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/testutil/codexinstalled"
)

// The three exact wire states of the list-only useStateDbOnly field. They are
// recorded as a tri-state rather than a bool because "omitted" is a distinct
// scan-and-repair baseline from explicit false, and the product must send
// neither.
const (
	wireStateDbOnlyTrue    = "true"
	wireStateDbOnlyFalse   = "false"
	wireStateDbOnlyOmitted = "omitted"
)

// wireRequest is one content-free client-to-server observation. It carries the
// JSON-RPC method name and, for a list request, the exact tri-state of the
// useStateDbOnly field. No other params field, and no response byte at all, is
// ever retained: this ledger is a protocol witness, not a transcript.
type wireRequest struct {
	Method      string
	StateDbOnly string
}

// wireLedger is a pass-through unix proxy in front of an exact private
// app-server socket. It forwards every byte verbatim in both directions and
// decodes a copy of the client-to-server WebSocket frames so the installed
// smoke can assert what the product actually put on the wire instead of
// inferring it from the client API it called.
type wireLedger struct {
	path      string
	upstream  string
	listener  net.Listener
	mu        sync.Mutex
	requests  []wireRequest
	failures  []error
	wg        sync.WaitGroup
	closeOnce sync.Once
}

func newWireLedger(socketPath, upstreamPath string) (*wireLedger, error) {
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("listen wire ledger: %w", err)
	}
	ledger := &wireLedger{path: socketPath, upstream: upstreamPath, listener: listener}
	ledger.wg.Add(1)
	go ledger.accept()
	return ledger, nil
}

func (l *wireLedger) Path() string { return l.path }

func (l *wireLedger) accept() {
	defer l.wg.Done()
	for {
		client, err := l.listener.Accept()
		if err != nil {
			return
		}
		l.wg.Go(func() {
			l.serve(client)
		})
	}
}

func (l *wireLedger) serve(client net.Conn) {
	defer func() { _ = client.Close() }()
	upstream, err := net.Dial("unix", l.upstream)
	if err != nil {
		l.recordFailure(fmt.Errorf("dial upstream: %w", err))
		return
	}
	defer func() { _ = upstream.Close() }()
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(client, upstream)
		done <- struct{}{}
	}()
	go func() {
		l.observeClientStream(upstream, client)
		done <- struct{}{}
	}()
	<-done
}

// observeClientStream forwards the client stream verbatim while decoding a
// copy. The handshake is line oriented and the frames that follow are the
// masked client frames of RFC 6455; both are written through before anything
// is decoded, so the ledger can never alter what the endpoint receives.
func (l *wireLedger) observeClientStream(dst io.Writer, src io.Reader) {
	reader := bufio.NewReader(src)
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			if _, writeErr := io.WriteString(dst, line); writeErr != nil {
				return
			}
		}
		if err != nil {
			return
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	var message []byte
	for {
		opcode, final, payload, err := readClientFrame(reader, dst)
		if err != nil {
			return
		}
		switch opcode {
		case 0x1:
			message = append(message[:0], payload...)
		case 0x0:
			message = append(message, payload...)
		default:
			// Control frames (ping/pong/close) carry no JSON-RPC message.
			continue
		}
		if final {
			l.observeMessage(message)
			message = message[:0]
		}
	}
}

// maxObservedFrame bounds only what the ledger decodes. Bytes are always
// forwarded in full; a frame beyond this size is passed through and skipped
// for observation rather than buffered.
const maxObservedFrame = 4 << 20

func readClientFrame(reader *bufio.Reader, dst io.Writer) (opcode byte, final bool, payload []byte, err error) {
	header := make([]byte, 2)
	if _, err = io.ReadFull(reader, header); err != nil {
		return 0, false, nil, err
	}
	if _, err = dst.Write(header); err != nil {
		return 0, false, nil, err
	}
	final = header[0]&0x80 != 0
	opcode = header[0] & 0x0f
	masked := header[1]&0x80 != 0
	length := int(header[1] & 0x7f)
	switch length {
	case 126:
		extended := make([]byte, 2)
		if _, err = io.ReadFull(reader, extended); err != nil {
			return 0, false, nil, err
		}
		if _, err = dst.Write(extended); err != nil {
			return 0, false, nil, err
		}
		length = int(extended[0])<<8 | int(extended[1])
	case 127:
		extended := make([]byte, 8)
		if _, err = io.ReadFull(reader, extended); err != nil {
			return 0, false, nil, err
		}
		if _, err = dst.Write(extended); err != nil {
			return 0, false, nil, err
		}
		length = 0
		for _, b := range extended {
			length = length<<8 | int(b)
		}
	}
	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(reader, mask[:]); err != nil {
			return 0, false, nil, err
		}
		if _, err = dst.Write(mask[:]); err != nil {
			return 0, false, nil, err
		}
	}
	if length < 0 {
		return 0, false, nil, errors.New("wire ledger: negative frame length")
	}
	body := make([]byte, length)
	if _, err = io.ReadFull(reader, body); err != nil {
		return 0, false, nil, err
	}
	if _, err = dst.Write(body); err != nil {
		return 0, false, nil, err
	}
	if length > maxObservedFrame {
		return opcode, final, nil, nil
	}
	if masked {
		for i := range body {
			body[i] ^= mask[i%len(mask)]
		}
	}
	return opcode, final, body, nil
}

func (l *wireLedger) observeMessage(message []byte) {
	if len(message) == 0 {
		return
	}
	var envelope struct {
		Method string `json:"method"`
		Params struct {
			UseStateDbOnly *bool `json:"useStateDbOnly"`
		} `json:"params"`
	}
	if err := json.Unmarshal(message, &envelope); err != nil {
		return
	}
	if strings.TrimSpace(envelope.Method) == "" {
		return
	}
	observed := wireRequest{Method: envelope.Method, StateDbOnly: wireStateDbOnlyOmitted}
	if envelope.Params.UseStateDbOnly != nil {
		if *envelope.Params.UseStateDbOnly {
			observed.StateDbOnly = wireStateDbOnlyTrue
		} else {
			observed.StateDbOnly = wireStateDbOnlyFalse
		}
	}
	l.mu.Lock()
	l.requests = append(l.requests, observed)
	l.mu.Unlock()
}

func (l *wireLedger) recordFailure(err error) {
	l.mu.Lock()
	l.failures = append(l.failures, err)
	l.mu.Unlock()
}

func (l *wireLedger) snapshot() []wireRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]wireRequest(nil), l.requests...)
}

func (l *wireLedger) failureCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.failures)
}

func (l *wireLedger) Close() error {
	var err error
	l.closeOnce.Do(func() {
		err = l.listener.Close()
		l.wg.Wait()
		if removeErr := os.Remove(l.path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	})
	return err
}

func listRequests(requests []wireRequest) []wireRequest {
	var listed []wireRequest
	for _, request := range requests {
		if request.Method == "thread/list" {
			listed = append(listed, request)
		}
	}
	return listed
}

// scaleFixture is the exact private generation plus the real-scale store copy
// both installed smokes need. The store is an operator-prepared read-only copy:
// the test never reads, writes, or names the live user store, and it never
// creates or deletes a session file anywhere.
type scaleFixture struct {
	fixture *codexinstalled.Fixture
	direct  *codexinstalled.DirectEndpoint
	version string
}

// scaleSmokeEnv resolves the three exact inputs of a real-scale installed
// smoke. The exact generation and the store copy are inputs rather than
// inferences so the recorded evidence names what actually answered.
func scaleSmokeEnv(t *testing.T, rootEnv, binaryEnv, versionEnv string) (root, binary, version string, enabled bool) {
	t.Helper()
	root, enabled, err := codexinstalled.SmokeRoot(rootEnv)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		return "", "", "", false
	}
	binary = strings.TrimSpace(os.Getenv(binaryEnv))
	if !filepath.IsAbs(binary) {
		t.Fatalf("%s must be an absolute path to the exact private generation", binaryEnv)
	}
	version = strings.TrimSpace(os.Getenv(versionEnv))
	if version == "" {
		t.Fatalf("%s must name the exact expected generation", versionEnv)
	}
	return root, binary, version, true
}

// startScaleFixture adopts the pre-copied CODEX_HOME under root and starts the
// exact private generation on the fixture-owned private listener. It asserts
// the running app-server is the exact generation named by the caller: a
// real-scale observation attributed to the wrong binary would be evidence for
// a contract nobody declared.
func startScaleFixture(ctx context.Context, t *testing.T, root, binary, version, label string) *scaleFixture {
	t.Helper()
	codexHome := filepath.Join(root, "codex-home")
	info, err := os.Stat(codexHome)
	if err != nil || !info.IsDir() {
		t.Fatalf("real-scale CODEX_HOME copy must exist at %s: %v", codexHome, err)
	}
	assertOutsideLiveStore(t, codexHome)
	selfReported, err := exactGenerationVersion(ctx, binary)
	if err != nil || selfReported != version {
		t.Fatalf("exact generation binary %s self-reports %q (err=%v), want %q", binary, selfReported, err, version)
	}
	t.Setenv("CODEX_HOME", codexHome)
	// The exact generation is also what LookPath resolves, so the fixture's own
	// version discovery and command shim describe the binary under test rather
	// than whatever the ambient PATH happens to carry.
	t.Setenv("PATH", filepath.Dir(binary)+string(os.PathListSeparator)+os.Getenv("PATH"))
	fixture, err := codexinstalled.NewExisting(root)
	if err != nil {
		t.Fatal(err)
	}
	fixture.ApplyEnv(t.Setenv)
	t.Cleanup(func() {
		if err := fixture.Cleanup(); err != nil {
			t.Errorf("%s fixture cleanup: %v", label, err)
		}
	})
	direct, ready := fixture.StartDirect(ctx, label, binary)
	if ready.Class != codexinstalled.ResultPass {
		logInstalledResult(t, ready)
		t.Fatalf("%s direct endpoint = %+v", label, ready)
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer closeCancel()
		if result := direct.Close(closeCtx); result.Class != codexinstalled.ResultPass {
			t.Errorf("%s direct close = %+v", label, result)
		}
	})
	if got := fixture.Versions().AppServer; got != version {
		t.Fatalf("running app-server generation = %q, want exact %q", got, version)
	}
	// The managed payload link is what lets a terminal result carry a complete
	// version tuple. It is the one artifact this smoke adds to the copy, and it
	// is removed again so the copy stays reusable and pristine.
	if provision := fixture.ProvisionManagedPayload(); provision.Class != codexinstalled.ResultPass {
		logInstalledResult(t, provision)
		t.Fatalf("%s managed payload provision = %+v", label, provision)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(filepath.Join(codexHome, "packages")); err != nil {
			t.Errorf("%s managed payload link cleanup: %v", label, err)
		}
	})
	return &scaleFixture{fixture: fixture, direct: direct, version: version}
}

// assertOutsideLiveStore refuses any path inside the operator's live agent
// stores. Every real-scale smoke reads a copy; nothing here may ever be
// pointed at the store a person actually resumes from.
func assertOutsideLiveStore(t *testing.T, path string) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve user home: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		t.Fatalf("resolve %s: %v", path, err)
	}
	for _, live := range []string{
		filepath.Join(home, ".codex"),
		filepath.Join(home, ".claude"),
		filepath.Join(home, ".gemini"),
	} {
		liveResolved, err := filepath.EvalSymlinks(live)
		if err != nil {
			continue
		}
		if resolved == liveResolved || strings.HasPrefix(resolved, liveResolved+string(filepath.Separator)) {
			t.Fatalf("fixture path %s resolves inside the live store %s", resolved, liveResolved)
		}
	}
}

// rolloutManifest is the mutation ledger for one rollout store: one entry per
// rollout file, content-free (path, size, modification time). Comparing it
// across a window shows exactly which rollout files a window rewrote, added,
// or removed. Only rollout files are tracked: an endpoint always writes its own
// sqlite bookkeeping, and conflating that with a rollout repair would make the
// ledger say nothing about the behavior under test.
func rolloutManifest(t *testing.T, sessionsRoot string) map[string]string {
	t.Helper()
	manifest := map[string]string{}
	err := filepath.Walk(sessionsRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		manifest[path] = fmt.Sprintf("%d/%d", info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		t.Fatalf("walk rollout store %s: %v", sessionsRoot, err)
	}
	return manifest
}

// rolloutDrift is the content-free difference between two manifests of the
// same store.
func rolloutDrift(before, after map[string]string) []string {
	var drift []string
	for path, state := range before {
		if got, ok := after[path]; !ok {
			drift = append(drift, "removed "+path)
		} else if got != state {
			drift = append(drift, "rewrote "+path)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			drift = append(drift, "added "+path)
		}
	}
	return drift
}

func assertNoRolloutDrift(t *testing.T, label string, before, after map[string]string) {
	t.Helper()
	if drift := rolloutDrift(before, after); len(drift) > 0 {
		t.Errorf("%s: rollout mutation ledger is not empty: %d entries, first=%q", label, len(drift), drift[0])
	}
}

// liveStoreGuard pins the operator's real rollout store for the duration of a
// smoke. Every real-scale observation runs against a copy, so the live store
// must be byte-identical when the smoke ends; this is the only place the live
// store is ever named, and it is only ever read.
func liveStoreGuard(t *testing.T) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve user home: %v", err)
	}
	live := filepath.Join(home, ".codex", "sessions")
	if info, err := os.Stat(live); err != nil || !info.IsDir() {
		t.Skipf("live rollout store %s is unavailable: %v", live, err)
	}
	before := rolloutManifest(t, live)
	t.Logf("live store guard: %s rollout_files=%d", live, len(before))
	t.Cleanup(func() {
		assertNoRolloutDrift(t, "live user rollout store", before, rolloutManifest(t, live))
	})
}

// TestInstalledStateDbOnlyCatalogListSettlesAtRealScale is the Phase 4
// replacement for what the Phase 0 A/B could not claim. That smoke owned an
// empty isolated CODEX_HOME, so it could only show field compatibility; this
// one runs the exact private generation against a read-only copy of a
// real-scale store and asserts the two things scale actually decides: that the
// product's explicit useStateDbOnly=true list either returns a well-formed
// result or a typed timeout inside the declared native window, and that the
// product put exactly one list request on the wire with that field true. The
// explicit-false and omitted arms are recorded afterwards as observation
// baselines only; they are never a product retry.
func TestInstalledStateDbOnlyCatalogListSettlesAtRealScale(t *testing.T) {
	root, binary, version, enabled := scaleSmokeEnv(t,
		"PROJMUX_CODEX_SCALE_CATALOG_SMOKE_ROOT",
		"PROJMUX_CODEX_SCALE_CATALOG_BINARY",
		"PROJMUX_CODEX_SCALE_CATALOG_VERSION")
	if !enabled {
		t.Skip("set PROJMUX_CODEX_SCALE_CATALOG_SMOKE_ROOT for the real-scale installed state-db-only smoke")
	}

	liveStoreGuard(t)
	codexHome := filepath.Join(root, "codex-home")
	sessions := filepath.Join(codexHome, "sessions")
	census := realScaleCensus(t, codexHome)
	t.Logf("real-scale census store=%s rollout_files=%d rollout_dirs=%d", codexHome, census.rolloutFiles, census.rolloutDirs)
	beforeProduct := rolloutManifest(t, sessions)
	if len(beforeProduct) != census.rolloutFiles {
		t.Fatalf("rollout manifest holds %d files, want the same-run census %d", len(beforeProduct), census.rolloutFiles)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	scale := startScaleFixture(ctx, t, root, binary, version, "installed-scale-catalog-smoke")
	t.Logf("exact private generation cli=%s app_server=%s", scale.fixture.Versions().CLI, scale.fixture.Versions().AppServer)

	ledger, err := newWireLedger(filepath.Join(root, "wire.sock"), scale.fixture.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := ledger.Close(); err != nil {
			t.Errorf("wire ledger close: %v", err)
		}
	})

	client, err := codexappserver.OpenPrivateUnix(ctx, ledger.Path(), 20*time.Second, "installed-scale-catalog-smoke", true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	// The product's own list, bounded by the declared native window. The
	// contract is that this settles inside the window as either an exact result
	// or a typed timeout -- never as a field or params refusal, which is what a
	// generation that did not accept useStateDbOnly would produce.
	nativeCtx, nativeCancel := context.WithTimeout(ctx, aisessionsNativeWindow)
	started := time.Now()
	page, listErr := client.ListCatalogThreads(nativeCtx, codexappserver.CatalogQuery{})
	trueElapsed := time.Since(started)
	nativeCancel()
	switch {
	case listErr == nil:
		t.Logf("real-scale arm=true rows=%d next_cursor=%t elapsed=%s", len(page.Threads), page.NextCursor != nil, trueElapsed)
	case errors.Is(listErr, codexappserver.ErrStateDbOnlyRejected):
		t.Fatalf("exact generation %s refused the explicit useStateDbOnly field: %v", version, listErr)
	case errors.Is(listErr, context.DeadlineExceeded):
		t.Logf("real-scale arm=true typed timeout inside the declared native window elapsed=%s", trueElapsed)
	default:
		t.Fatalf("real-scale arm=true settled on neither an exact result nor a typed timeout after %s: %v", trueElapsed, listErr)
	}
	if trueElapsed > aisessionsNativeWindow+250*time.Millisecond {
		t.Errorf("real-scale arm=true settled in %s, outside the declared native window %s", trueElapsed, aisessionsNativeWindow)
	}

	// The wire ledger is read before the observation baselines run, so the
	// snapshot contains the product path and nothing else.
	productWire := listRequests(ledger.snapshot())
	if len(productWire) != 1 {
		t.Fatalf("product path put %d thread/list requests on the wire, want exactly 1: %+v", len(productWire), productWire)
	}
	if productWire[0].StateDbOnly != wireStateDbOnlyTrue {
		t.Fatalf("product thread/list useStateDbOnly = %q, want %q", productWire[0].StateDbOnly, wireStateDbOnlyTrue)
	}
	// The state-db-only guarantee is a read: the product path must settle the
	// catalog without repairing a single rollout file.
	afterProduct := rolloutManifest(t, sessions)
	assertNoRolloutDrift(t, "state-db-only product path", beforeProduct, afterProduct)

	explicitFalse := false
	for _, arm := range []struct {
		name  string
		field *bool
	}{
		{name: wireStateDbOnlyFalse, field: &explicitFalse},
		{name: wireStateDbOnlyOmitted, field: nil},
	} {
		params := stateDbOnlyListParams{
			Limit: codexappserver.DefaultCatalogPageSize, SortKey: "recency_at", SortDirection: "desc",
			SourceKinds: []string{"cli", "vscode", "appServer"}, Archived: false, UseStateDbOnly: arm.field,
		}
		var baseline stateDbOnlyListResult
		armCtx, armCancel := context.WithTimeout(ctx, 30*time.Second)
		started := time.Now()
		err := client.Request(armCtx, "thread/list", params, &baseline)
		elapsed := time.Since(started)
		armCancel()
		if err != nil {
			t.Logf("real-scale baseline arm=%s refused elapsed=%s: %v", arm.name, elapsed, err)
			continue
		}
		t.Logf("real-scale baseline arm=%s rows=%d next_cursor=%t elapsed=%s",
			arm.name, len(baseline.Data), baseline.NextCursor != nil, elapsed)
	}

	// Every list on this connection is accounted for: one product request with
	// the field true, then exactly the two observation baselines this test
	// issued itself. A product retry with false or omitted would show up here
	// as a fourth entry or as a second true-armed request.
	finalWire := listRequests(ledger.snapshot())
	if len(finalWire) != 3 {
		t.Fatalf("wire ledger recorded %d thread/list requests, want the product request plus two baselines: %+v", len(finalWire), finalWire)
	}
	if finalWire[0].StateDbOnly != wireStateDbOnlyTrue ||
		finalWire[1].StateDbOnly != wireStateDbOnlyFalse ||
		finalWire[2].StateDbOnly != wireStateDbOnlyOmitted {
		t.Fatalf("wire ledger tri-state order = %+v, want true then false then omitted", finalWire)
	}
	if count := ledger.failureCount(); count != 0 {
		t.Errorf("wire ledger recorded %d proxy failures, want 0", count)
	}

	// What the baseline arms spend their extra time on is recorded, not
	// asserted: whether a given run finds anything left to repair depends on
	// the state the copy is already in. The asserted half is above -- the
	// product path rewrites nothing.
	baselineDrift := rolloutDrift(afterProduct, rolloutManifest(t, sessions))
	t.Logf("scan-and-repair observation: baseline arms rewrote %d rollout files, product path rewrote %d",
		len(baselineDrift), len(rolloutDrift(beforeProduct, afterProduct)))

	result := codexinstalled.NewResult(scale.fixture.Versions(), codexinstalled.TopologyDirect,
		codexinstalled.StageReady, codexinstalled.ResultPass, "state-db-only-real-scale-settled")
	logInstalledResult(t, result)
}

// aisessionsNativeWindow mirrors the declared Codex native page bound. The
// installed smoke asserts settlement inside that declared window rather than
// re-deriving one, so a budget change is a product decision and never an
// accidental relaxation here.
const aisessionsNativeWindow = 300 * time.Millisecond

type realScaleStoreCensus struct {
	rolloutFiles int
	rolloutDirs  int
}

// realScaleCensus records what the copy actually holds at the instant this run
// starts. The planning literals of this track no longer describe the store,
// so every observation is compared against this same-run census instead.
func realScaleCensus(t *testing.T, codexHome string) realScaleStoreCensus {
	t.Helper()
	census := realScaleStoreCensus{}
	sessions := filepath.Join(codexHome, "sessions")
	err := filepath.Walk(sessions, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			census.rolloutDirs++
		case info.Mode().IsRegular() && strings.HasSuffix(path, ".jsonl"):
			census.rolloutFiles++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("census real-scale rollout store: %v", err)
	}
	if census.rolloutFiles == 0 {
		t.Fatalf("real-scale census found no rollout files under %s", sessions)
	}
	return census
}

// exactGenerationVersion reports the version an exact binary self-reports. It
// exists so the smoke can refuse a mislabeled input instead of attributing a
// real-scale observation to a generation that never ran.
func exactGenerationVersion(ctx context.Context, binary string) (string, error) {
	command := exec.CommandContext(ctx, binary, "--version") // #nosec G204 -- explicit absolute test input.
	output, err := command.CombinedOutput()
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(output))
	if len(fields) == 0 {
		return "", errors.New("empty version output")
	}
	return fields[len(fields)-1], nil
}
