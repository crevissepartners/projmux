package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// claudeRegistrationChain lists every function of the registration chain and
// the spellings, besides a reason constant, its reason result may take: the
// success value and propagation of a reason another swept function returned.
// A call to a chain function and refuseClaudeRegistration(<allowed>) are
// accepted everywhere; anything else -- a bare false, errors.New, a raw err --
// is an untagged refusal.
var claudeRegistrationChain = map[string][]string{
	"claudeEndpointRegistrationHook": {"reason"},
	"claudeRegistrationBootstrap":    {"claudeRegistrationProceed"},
	"registerClaudeEndpoint":         {"claudeRegistrationProceed", "refusal.reason"},
	"startClaudeEndpointHelper":      {"nil"},
	"claudeEndpointHelper":           {"reason"},
	"serveClaudeRegistration":        {"reason"},
	"claudeRegistrationRoute":        {"claudeRegistrationProceed"},
	// The claim wrapper forwards transaction errors to the serving function's
	// reason classifier; its callback must still carry a tagged refusal.
	"registerClaudeEndpointBootstrap":     {"err", `registerClaudeProcessHelper(bootstrap, "register")`},
	"admitClaudeRegistration":             {"nil"},
	"claudeRegistrationClaimRefusal":      nil,
	"claudeRegistrationTransactionReason": {"refusal.reason"},
}

// claudeRegistrationReasonConstants maps each ClaudeRegistrationReason
// constant name declared in the diagnostics package onto its value.
func claudeRegistrationReasonConstants(t *testing.T) map[string]string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join("..", "diagnostics", "claude_registration.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value := spec.(*ast.ValueSpec)
			if ident, ok := value.Type.(*ast.Ident); !ok || ident.Name != "ClaudeRegistrationReason" {
				continue
			}
			for i, name := range value.Names {
				literal, err := strconv.Unquote(value.Values[i].(*ast.BasicLit).Value)
				if err != nil {
					t.Fatal(err)
				}
				out[name.Name] = literal
			}
		}
	}
	return out
}

// claudeRegistrationSweep checks every return of every chain function, and of
// the claim transaction's callback, in src. It returns each untagged return
// and the reason constants the file names anywhere.
func claudeRegistrationSweep(t *testing.T, src []byte, constants map[string]string) (failures []string, used map[string]bool) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "claude_endpoint.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	used = map[string]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		if selector, ok := node.(*ast.SelectorExpr); ok {
			if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == "diagnostics" {
				if _, ok := constants[selector.Sel.Name]; ok {
					used[selector.Sel.Name] = true
				}
			}
		}
		return true
	})
	var tagged func(expr ast.Expr, extras []string) bool
	tagged = func(expr ast.Expr, extras []string) bool {
		if slices.Contains(extras, types.ExprString(expr)) {
			return true
		}
		switch value := expr.(type) {
		case *ast.SelectorExpr:
			pkg, ok := value.X.(*ast.Ident)
			_, constant := constants[value.Sel.Name]
			return ok && pkg.Name == "diagnostics" && constant
		case *ast.CallExpr:
			name, ok := value.Fun.(*ast.Ident)
			if !ok {
				return false
			}
			if name.Name == "refuseClaudeRegistration" {
				return len(value.Args) == 1 && tagged(value.Args[0], extras)
			}
			_, chain := claudeRegistrationChain[name.Name]
			return chain
		}
		return false
	}
	var sweep func(owner string, body *ast.BlockStmt, extras []string) (returns, callbacks int)
	sweep = func(owner string, body *ast.BlockStmt, extras []string) (returns, callbacks int) {
		ast.Inspect(body, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.FuncLit, *ast.DeferStmt:
				// Readiness predicates and the deferred clear are not refusals.
				return false
			case *ast.CallExpr:
				if selector, ok := value.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "UpdateConvergent" {
					for _, arg := range value.Args {
						if callback, ok := arg.(*ast.FuncLit); ok {
							callbacks++
							r, _ := sweep(owner+" transaction callback", callback.Body, nil)
							if r == 0 {
								failures = append(failures, owner+": transaction callback has no return")
							}
						}
					}
				}
			case *ast.ReturnStmt:
				returns++
				if len(value.Results) == 0 || !tagged(value.Results[len(value.Results)-1], extras) {
					failures = append(failures, fmt.Sprintf("%s: untagged return at %s", owner, fset.Position(value.Pos())))
				}
			}
			return true
		})
		return returns, callbacks
	}
	found := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil {
			continue
		}
		extras, chain := claudeRegistrationChain[fn.Name.Name]
		if !chain {
			continue
		}
		found[fn.Name.Name] = true
		returns, callbacks := sweep(fn.Name.Name, fn.Body, extras)
		if returns == 0 {
			failures = append(failures, fn.Name.Name+": no return swept")
		}
		if want := fn.Name.Name == "registerClaudeEndpointBootstrap"; want != (callbacks == 1) {
			failures = append(failures, fmt.Sprintf("%s: %d claim transaction callbacks", fn.Name.Name, callbacks))
		}
	}
	for name := range claudeRegistrationChain {
		if !found[name] {
			failures = append(failures, name+": chain function missing")
		}
	}
	return failures, used
}

// TestClaudeRegistrationSweepTagsEveryRefusal (A1) sweeps the registration
// chain: every return carries a reason from the closed table, every table
// reason is named by the chain, and an untagged refusal is reported.
func TestClaudeRegistrationSweepTagsEveryRefusal(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("claude_endpoint.go")
	if err != nil {
		t.Fatal(err)
	}
	constants := claudeRegistrationReasonConstants(t)
	failures, used := claudeRegistrationSweep(t, src, constants)
	if len(failures) > 0 {
		t.Fatalf("untagged registration returns:\n%s", strings.Join(failures, "\n"))
	}
	usedValues := map[string]bool{}
	for name := range used {
		usedValues[constants[name]] = true
	}
	// The diagnostics tests hold these constants and the recorder's table to
	// the same set of unique reasons.
	for name, reason := range constants {
		if !usedValues[reason] {
			t.Errorf("reason %s (%q) is in the table but no registration site names it", name, reason)
		}
	}

	for name, mutation := range map[string][2]string{
		"bootstrap bare value": {"return matched, diagnostics.ClaudeRegistrationNonceUnavailable", `return matched, "nonce"`},
		"start raw error": {"return refuseClaudeRegistration(diagnostics.ClaudeRegistrationHelperAckPipe)",
			`return errors.New("claude helper acknowledgement unavailable")`},
		"serve untagged": {"return diagnostics.ClaudeRegistrationStaleBeforeAck", "return claudeRegistrationProceed"},
		"route untagged": {"return tmuxClaudeRouteResolver(bootstrap.RegistryPath, bootstrap.AgentUID), claudeRegistrationProceed",
			`return tmuxClaudeRouteResolver(bootstrap.RegistryPath, bootstrap.AgentUID), "ready"`},
		"claim raw error": {"return refuseClaudeRegistration(diagnostics.ClaudeRegistrationProviderProcessGone)",
			`return errors.New("claude provider process is unavailable")`},
		"transaction callback": {"return admitClaudeRegistration(reg, bootstrap, mutator)", "return err"},
	} {
		t.Run("negative control: "+name, func(t *testing.T) {
			if !bytes.Contains(src, []byte(mutation[0])) {
				t.Fatalf("control expects %q in claude_endpoint.go", mutation[0])
			}
			mutated := bytes.Replace(src, []byte(mutation[0]), []byte(mutation[1]), 1)
			if failures, _ := claudeRegistrationSweep(t, mutated, constants); len(failures) == 0 {
				t.Fatal("sweep did not report the untagged return")
			}
		})
	}
}

// TestClaudeRegistrationEveryReasonReadsBackAndRenders (A1/A4) appends each
// table reason from each process allowed to record it into a real journal,
// reads every record back, and renders it for `diagnostics log`.
func TestClaudeRegistrationEveryReasonReadsBackAndRenders(t *testing.T) {
	t.Parallel()
	var reasons []diagnostics.ClaudeRegistrationReason
	for _, reason := range claudeRegistrationReasonConstants(t) {
		// Only recordable reasons reach the journal; see
		// TestClaudeRegistrationUnmanagedSessionWritesNothing.
		if diagnostics.ClaudeRegistrationReason(reason).Recorded() {
			reasons = append(reasons, diagnostics.ClaudeRegistrationReason(reason))
		}
	}
	store := diagnostics.NewStore(filepath.Join(t.TempDir(), "operations.jsonl"))
	recorder := diagnostics.NewLifecycleRecorder(store, "claude-registration-run", "1.0.0", "tmux").ClaudeRegistration()
	want := 0
	for _, reason := range reasons {
		for _, source := range []diagnostics.ClaudeRegistrationSource{diagnostics.ClaudeRegistrationSourceHook, diagnostics.ClaudeRegistrationSourceHelper} {
			recorder.Record(diagnostics.ClaudeRegistrationRecord{Source: source, Reason: reason, AgentUID: "agent-01", PaneUID: "pane-01"})
		}
	}
	events, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, event := range events {
		line := formatOperationalEvent(event)
		if !strings.Contains(line, " agent agent.claude.registration ") || !strings.Contains(line, "source="+event.Source) ||
			!strings.Contains(line, "code="+event.Code) || !strings.Contains(line, "agent_uid=agent-01") || !strings.Contains(line, "pane_uid=pane-01") {
			t.Fatalf("rendered %q", line)
		}
		seen[event.Code] = true
	}
	for _, reason := range reasons {
		if !seen[reason.Code()] {
			t.Errorf("reason %q never read back", reason)
		}
		want++
	}
	if len(seen) != want {
		t.Fatalf("read back %d codes, want %d", len(seen), want)
	}
}

// claudeRegistrationJournal is an in-memory journal whose appends can run a
// check first and fail on demand.
type claudeRegistrationJournal struct {
	mu       sync.Mutex
	events   []diagnostics.Event
	onAppend func(diagnostics.Event)
	err      error
}

func (j *claudeRegistrationJournal) Append(event diagnostics.Event) error {
	if j.onAppend != nil {
		j.onAppend(event)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.events = append(j.events, event)
	return j.err
}

func (j *claudeRegistrationJournal) snapshot() []diagnostics.Event {
	j.mu.Lock()
	defer j.mu.Unlock()
	return slices.Clone(j.events)
}

func (j *claudeRegistrationJournal) recorder() *diagnostics.ClaudeRegistrationRecorder {
	return diagnostics.NewLifecycleRecorder(j, "claude-registration-run", "1.0.0", "tmux").ClaudeRegistration()
}

func assertClaudeRegistrationRecords(t *testing.T, got []diagnostics.Event, source diagnostics.ClaudeRegistrationSource, subject claudeRegistrationSubject, reasons ...diagnostics.ClaudeRegistrationReason) {
	t.Helper()
	if len(got) != len(reasons) {
		t.Fatalf("records = %+v, want %v", got, reasons)
	}
	for i, event := range got {
		if event.Event != "agent.claude.registration" || event.Source != string(source) || event.Code != reasons[i].Code() ||
			event.AgentUID != subject.AgentUID || event.PaneUID != subject.PaneUID {
			t.Fatalf("record %d = %+v, want %s %s %+v", i, event, source, reasons[i], subject)
		}
		if reasons[i].Refusal() != (event.Result == "error") {
			t.Fatalf("record %d result %q for %s", i, event.Result, reasons[i])
		}
	}
}

// TestClaudeRegistrationHookRouteRefusalsRecordOnce (A2/A3) drives the hook's
// route refusals and a payload refusal: each writes exactly one record, before
// any helper start, with no UIDs.
func TestClaudeRegistrationHookRouteRefusalsRecordOnce(t *testing.T) {
	t.Parallel()
	exact := intmetadata.PathFor(filepath.Join(t.TempDir(), "state"))
	corrupt := intmetadata.PathFor(filepath.Join(t.TempDir(), "state"))
	if err := os.MkdirAll(filepath.Dir(corrupt), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(corrupt, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		args  []string
		path  string
		stdin string
		want  diagnostics.ClaudeRegistrationReason
	}{
		{"arguments", []string{"extra"}, exact, "", diagnostics.ClaudeRegistrationHookArguments},
		{"relative registry path", nil, "state/registry.json", "", diagnostics.ClaudeRegistrationRegistryPathInvalid},
		{"unreadable registry", nil, corrupt, "", diagnostics.ClaudeRegistrationRegistryUnreadable},
		{"oversized input", nil, exact, strings.Repeat("x", 64*1024+1), diagnostics.ClaudeRegistrationHookInputUnreadable},
		{"not SessionStart", nil, exact, `{"hook_event_name":"Stop","session_id":"s"}`, diagnostics.ClaudeRegistrationPayloadNotSessionStart},
		{"unmanaged pane", nil, exact, `{"hook_event_name":"SessionStart","session_id":"s"}`, diagnostics.ClaudeRegistrationPaneBindingMismatch},
	} {
		t.Run(test.name, func(t *testing.T) {
			journal := &claudeRegistrationJournal{}
			env := func(key string) string {
				if key == internalClaudeRegistryPathEnv {
					return test.path
				}
				return ""
			}
			start := func(claudeEndpointBootstrap) error { t.Fatal("hook started a helper after a refusal"); return nil }
			if err := recordClaudeEndpointRegistration(journal.recorder(), test.args, env, strings.NewReader(test.stdin), os.Getpid(), start); err != nil {
				t.Fatalf("hook returned %v", err)
			}
			assertClaudeRegistrationRecords(t, journal.snapshot(), diagnostics.ClaudeRegistrationSourceHook, claudeRegistrationSubject{}, test.want)
		})
	}
}

// claudeRegistrationHookEnv is the managed activation environment of the
// fixture's SessionStart hook.
func claudeRegistrationHookEnv(f *claudeEndpointTestFixture) map[string]string {
	return map[string]string{internalClaudeRegistryPathEnv: f.bootstrap.RegistryPath, internalActivationPaneUIDEnv: f.bootstrap.PaneUID,
		internalActivationGenerationEnv: f.bootstrap.Generation, "CLAUDE_CODE_MESSAGING_SOCKET": f.bootstrap.Socket,
		"CLAUDE_CODE_MESSAGING_TOKEN": f.bootstrap.Token}
}

// TestClaudeRegistrationHookRecordsAtAllowedPointsOnly (A2/A3/A4) runs the
// managed hook: a confirmed admission records nothing, a start refusal records
// once after the start returned, UIDs appear only once the pane and agent
// matched the Registry, no credential or session id reaches the journal, and a
// failing journal leaves the result and the helper's bootstrap unchanged.
func TestClaudeRegistrationHookRecordsAtAllowedPointsOnly(t *testing.T) {
	t.Parallel()
	f := newClaudeEndpointTestFixture(t)
	matched := claudeRegistrationSubject{AgentUID: f.bootstrap.AgentUID, PaneUID: f.bootstrap.PaneUID}
	if !strings.HasPrefix(matched.AgentUID, "agent-") || !strings.HasPrefix(matched.PaneUID, "pane-") {
		t.Fatalf("fixture UIDs %+v are not opaque Registry UIDs", matched)
	}
	const session = "private-session-identity-for-residue-scan"
	payload := `{"hook_event_name":"SessionStart","session_id":"` + session + `"}`
	run := func(t *testing.T, journal *claudeRegistrationJournal, env map[string]string, parent int, start func(claudeEndpointBootstrap) error) {
		t.Helper()
		if err := recordClaudeEndpointRegistration(journal.recorder(), nil, func(key string) string { return env[key] }, strings.NewReader(payload), parent, start); err != nil {
			t.Fatalf("hook returned %v", err)
		}
	}

	var admitted []claudeEndpointBootstrap
	journal := &claudeRegistrationJournal{}
	run(t, journal, claudeRegistrationHookEnv(f), f.provider.Process.Pid, func(bootstrap claudeEndpointBootstrap) error {
		admitted = append(admitted, bootstrap)
		return nil
	})
	if len(admitted) != 1 || len(journal.snapshot()) != 0 {
		t.Fatalf("confirmed admission: starts=%d records=%+v, want one start and no hook record", len(admitted), journal.snapshot())
	}

	for _, test := range []struct {
		name string
		err  error
		want diagnostics.ClaudeRegistrationReason
	}{
		{"unconfirmed", refuseClaudeRegistration(diagnostics.ClaudeRegistrationHelperUnconfirmed), diagnostics.ClaudeRegistrationHelperUnconfirmed},
		{"executable", refuseClaudeRegistration(diagnostics.ClaudeRegistrationHelperExecutable), diagnostics.ClaudeRegistrationHelperExecutable},
		{"untyped", errors.New("claude helper start failed"), diagnostics.ClaudeRegistrationHelperStartFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			returned := false
			journal := &claudeRegistrationJournal{onAppend: func(diagnostics.Event) {
				if !returned {
					t.Error("hook appended before its helper start returned")
				}
			}}
			run(t, journal, claudeRegistrationHookEnv(f), f.provider.Process.Pid, func(claudeEndpointBootstrap) error {
				returned = true
				return test.err
			})
			assertClaudeRegistrationRecords(t, journal.snapshot(), diagnostics.ClaudeRegistrationSourceHook, matched, test.want)
		})
	}

	t.Run("failing journal", func(t *testing.T) {
		var starts []claudeEndpointBootstrap
		journal := &claudeRegistrationJournal{err: errors.New("fixture journal unavailable")}
		run(t, journal, claudeRegistrationHookEnv(f), f.provider.Process.Pid, func(bootstrap claudeEndpointBootstrap) error {
			starts = append(starts, bootstrap)
			return refuseClaudeRegistration(diagnostics.ClaudeRegistrationHelperUnconfirmed)
		})
		if len(starts) != 1 || len(journal.snapshot()) != 1 {
			t.Fatalf("starts=%d appends=%d, want one of each", len(starts), len(journal.snapshot()))
		}
		// The nonce is fresh per bootstrap; everything else the helper reads
		// is what the working journal's run handed it.
		got, want := starts[0], admitted[0]
		got.Registration.Authority.RegistrationGeneration, want.Registration.Authority.RegistrationGeneration = "", ""
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(want)
		if !bytes.Equal(gotJSON, wantJSON) {
			t.Fatal("a failing journal changed the helper's bootstrap")
		}
	})

	journalPath := filepath.Join(t.TempDir(), "operations.jsonl")
	store := diagnostics.NewStore(journalPath)
	recorder := diagnostics.NewLifecycleRecorder(store, "claude-registration-run", "1.0.0", "tmux").ClaudeRegistration()
	stale := claudeRegistrationHookEnv(f)
	stale[internalActivationGenerationEnv] = "old"
	noToken := claudeRegistrationHookEnv(f)
	noToken["CLAUDE_CODE_MESSAGING_TOKEN"] = ""
	for _, run := range []struct {
		env    map[string]string
		parent int
	}{{stale, f.provider.Process.Pid}, {claudeRegistrationHookEnv(f), os.Getpid()}, {noToken, f.provider.Process.Pid}} {
		env := run.env
		if err := recordClaudeEndpointRegistration(recorder, nil, func(key string) string { return env[key] }, strings.NewReader(payload), run.parent,
			func(claudeEndpointBootstrap) error { t.Fatal("refused bootstrap started a helper"); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	events, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	assertClaudeRegistrationRecords(t, events[:1], diagnostics.ClaudeRegistrationSourceHook, claudeRegistrationSubject{}, diagnostics.ClaudeRegistrationPaneBindingMismatch)
	assertClaudeRegistrationRecords(t, events[1:], diagnostics.ClaudeRegistrationSourceHook, matched,
		diagnostics.ClaudeRegistrationProviderProcessMismatch, diagnostics.ClaudeRegistrationMessagingEnvInvalid)
	journalBytes, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{session, f.bootstrap.Token, f.bootstrap.Socket, f.bootstrap.RegistryPath, strconv.Itoa(f.provider.Process.Pid)} {
		if bytes.Contains(journalBytes, []byte(private)) {
			t.Fatalf("journal carries a private value: %s", journalBytes)
		}
	}
}

// TestClaudeRegistrationUnmanagedSessionWritesNothing pins that a Claude
// session projmux did not launch -- no activation Registry path at all, as the
// user-wide SessionStart hook sees it -- stops as unmanaged-session and writes
// no record, directly and through the real `internal` dispatch with the
// variable empty and unset.
func TestClaudeRegistrationUnmanagedSessionWritesNothing(t *testing.T) {
	start := func(claudeEndpointBootstrap) error { t.Fatal("unmanaged session started a helper"); return nil }
	if subject, reason := claudeEndpointRegistrationHook(nil, func(string) string { return "" }, strings.NewReader(""), os.Getpid(), start); reason != diagnostics.ClaudeRegistrationUnmanagedSession || subject != (claudeRegistrationSubject{}) {
		t.Fatalf("unmanaged hook = %q %+v", reason, subject)
	}
	if reason := diagnostics.ClaudeRegistrationUnmanagedSession; reason.Recorded() || reason.Refusal() {
		t.Fatal("unmanaged-session is recordable")
	}
	for _, unset := range []bool{false, true} {
		t.Setenv(internalClaudeRegistryPathEnv, "")
		if unset {
			if err := os.Unsetenv(internalClaudeRegistryPathEnv); err != nil {
				t.Fatal(err)
			}
		}
		store := diagnostics.NewStore(filepath.Join(t.TempDir(), "operations.jsonl"))
		command := newInternalCommand()
		command.claudeRegistration = diagnostics.NewLifecycleRecorder(store, "claude-registration-run", "1.0.0", "tmux").ClaudeRegistration()
		var stdout, stderr bytes.Buffer
		if err := command.Run([]string{"claude-endpoint-register"}, &stdout, &stderr); err != nil || stdout.Len() != 0 || stderr.Len() != 0 {
			t.Fatalf("unset=%v: err=%v stdout=%q stderr=%q", unset, err, stdout.String(), stderr.String())
		}
		if events, err := store.Read(); err != nil || len(events) != 0 {
			t.Fatalf("unset=%v: unmanaged session wrote %+v (err %v)", unset, events, err)
		}
	}
}

// TestClaudeRegistrationHookRouteOutputUnchangedByFailingJournal (A3) runs the
// real `internal claude-endpoint-register` dispatch with a journal that fails
// every append: the route still prints nothing and returns nil.
func TestClaudeRegistrationHookRouteOutputUnchangedByFailingJournal(t *testing.T) {
	t.Setenv(internalClaudeRegistryPathEnv, "state/registry.json")
	journal := &claudeRegistrationJournal{err: errors.New("fixture journal unavailable")}
	command := newInternalCommand()
	command.claudeRegistration = journal.recorder()
	var stdout, stderr bytes.Buffer
	if err := command.Run([]string{"claude-endpoint-register"}, &stdout, &stderr); err != nil {
		t.Fatalf("hook route returned %v", err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("hook route printed stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	assertClaudeRegistrationRecords(t, journal.snapshot(), diagnostics.ClaudeRegistrationSourceHook, claudeRegistrationSubject{},
		diagnostics.ClaudeRegistrationRegistryPathInvalid)
}

// TestRegisterClaudeEndpointReturnsTheStartReason (A2) pins the hook's start
// seam: a confirmed start proceeds, a refusal keeps its reason, and anything
// else is a failed start.
func TestRegisterClaudeEndpointReturnsTheStartReason(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		err  error
		want diagnostics.ClaudeRegistrationReason
	}{
		{nil, claudeRegistrationProceed},
		{refuseClaudeRegistration(diagnostics.ClaudeRegistrationHelperAckPipe), diagnostics.ClaudeRegistrationHelperAckPipe},
		{fmt.Errorf("wrapped: %w", refuseClaudeRegistration(diagnostics.ClaudeRegistrationHelperBootstrap)), diagnostics.ClaudeRegistrationHelperBootstrap},
		{refuseClaudeRegistration(diagnostics.ClaudeRegistrationReady), diagnostics.ClaudeRegistrationHelperStartFailed},
		{errors.New("claude helper start failed"), diagnostics.ClaudeRegistrationHelperStartFailed},
	} {
		if got := registerClaudeEndpoint(claudeEndpointBootstrap{}, func(claudeEndpointBootstrap) error { return test.err }); got != test.want {
			t.Fatalf("start error %v -> %q, want %q", test.err, got, test.want)
		}
	}
	if !errors.Is(awaitClaudeHelperAdmission(&claudeClosedAck{}, time.Now(), &fakeClaudeHelperProcess{}), errClaudeHelperAdmissionUnconfirmed) {
		t.Fatal("an unacknowledged admission is not the unconfirmed refusal")
	}
}

type claudeClosedAck struct{}

func (claudeClosedAck) Read([]byte) (int, error)        { return 0, io.EOF }
func (claudeClosedAck) SetReadDeadline(time.Time) error { return nil }

// claudeHelperAckPipe is the hook's read side and the helper's fd 3.
func claudeHelperAckPipe(t *testing.T) (read, write *os.File) {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = read.Close(); _ = write.Close() })
	return read, write
}

// claudeHelperAckClosed reports whether the hook's read side is at EOF now,
// after any acknowledgement byte already written.
func claudeHelperAckClosed(read *os.File) bool {
	_ = read.SetReadDeadline(time.Now().Add(time.Second))
	_, err := io.ReadAll(read)
	return err == nil
}

// TestClaudeRegistrationHelperRecordsAfterTheAckCloses (A2/A3) drives the
// helper's route refusals, a serve refusal, and a Ready helper's end. Each
// refusal is one record; Ready is one ready record then one ended record; and
// every record but ready is appended only after the hook's read side sees EOF.
func TestClaudeRegistrationHelperRecordsAfterTheAckCloses(t *testing.T) {
	t.Parallel()
	bootstrap := claudeEndpointBootstrap{AgentUID: "agent-01", PaneUID: "pane-01", Token: "private-token-for-residue-scan"}
	input, err := json.Marshal(bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	noEnv := func(string) (string, bool) { return "", false }
	regular, err := os.Create(filepath.Join(t.TempDir(), "ack"))
	if err != nil {
		t.Fatal(err)
	}
	closed, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	subject := claudeRegistrationSubject{AgentUID: "agent-01", PaneUID: "pane-01"}
	for _, test := range []struct {
		name     string
		args     []string
		env      func(string) (string, bool)
		ack      func(write *os.File) *os.File
		stdin    string
		producer bool
		serve    diagnostics.ClaudeRegistrationReason
		subject  claudeRegistrationSubject
		want     []diagnostics.ClaudeRegistrationReason
	}{
		{name: "arguments", args: []string{"x"}, want: []diagnostics.ClaudeRegistrationReason{diagnostics.ClaudeRegistrationHelperArguments}},
		{name: "credential env", env: func(key string) (string, bool) { return "x", key == "CLAUDE_CODE_MESSAGING_TOKEN" },
			want: []diagnostics.ClaudeRegistrationReason{diagnostics.ClaudeRegistrationHelperArguments}},
		{name: "no fd 3", ack: func(*os.File) *os.File { return nil }, want: []diagnostics.ClaudeRegistrationReason{diagnostics.ClaudeRegistrationHelperAckMissing}},
		{name: "closed fd 3", ack: func(*os.File) *os.File { return closed }, want: []diagnostics.ClaudeRegistrationReason{diagnostics.ClaudeRegistrationHelperAckMissing}},
		{name: "fd 3 not a pipe", ack: func(*os.File) *os.File { return regular }, want: []diagnostics.ClaudeRegistrationReason{diagnostics.ClaudeRegistrationHelperAckNotPipe}},
		{name: "bad input", stdin: "{", want: []diagnostics.ClaudeRegistrationReason{diagnostics.ClaudeRegistrationHelperInput}},
		{name: "oversized input", stdin: strings.Repeat(" ", 64*1024+1), want: []diagnostics.ClaudeRegistrationReason{diagnostics.ClaudeRegistrationHelperInput}},
		{name: "producer mismatch", stdin: string(input), want: []diagnostics.ClaudeRegistrationReason{diagnostics.ClaudeRegistrationProducerMismatch}},
		{name: "serve refusal", stdin: string(input), producer: true, serve: diagnostics.ClaudeRegistrationStaleBeforeAck, subject: subject,
			want: []diagnostics.ClaudeRegistrationReason{diagnostics.ClaudeRegistrationStaleBeforeAck}},
		{name: "ready then ended", stdin: string(input), producer: true, serve: diagnostics.ClaudeRegistrationEndedNotCurrent, subject: subject,
			want: []diagnostics.ClaudeRegistrationReason{diagnostics.ClaudeRegistrationReady, diagnostics.ClaudeRegistrationEndedNotCurrent}},
	} {
		t.Run(test.name, func(t *testing.T) {
			read, write := claudeHelperAckPipe(t)
			journal := &claudeRegistrationJournal{}
			journal.onAppend = func(event diagnostics.Event) {
				// Without a usable fd 3 the pipe was never the helper's.
				if test.ack == nil && event.Code != diagnostics.ClaudeRegistrationReady.Code() && !claudeHelperAckClosed(read) {
					t.Errorf("%s appended while the hook's acknowledgement was still open", event.Code)
				}
			}
			env := test.env
			if env == nil {
				env = noEnv
			}
			openAck := func() *os.File { return write }
			if test.ack != nil {
				openAck = func() *os.File { return test.ack(write) }
			}
			served := 0
			in := claudeEndpointHelperInput{args: test.args, lookupEnv: env, stdin: strings.NewReader(test.stdin), openAck: openAck,
				producer: func(got claudeEndpointBootstrap) bool { return test.producer && got.Token == bootstrap.Token },
				serve: func(_ claudeEndpointBootstrap, ack io.Writer, admitted func()) diagnostics.ClaudeRegistrationReason {
					served++
					if !test.serve.Refusal() {
						_, _ = ack.Write([]byte{1})
						admitted()
					}
					return test.serve
				}}
			if err := recordClaudeEndpointHelper(journal.recorder(), in); err != nil {
				t.Fatalf("helper returned %v", err)
			}
			assertClaudeRegistrationRecords(t, journal.snapshot(), diagnostics.ClaudeRegistrationSourceHelper, test.subject, test.want...)
			if want := 0; test.producer && served != 1 || !test.producer && served != want {
				t.Fatalf("serve ran %d times", served)
			}
		})
	}
}

// TestClaudeRegistrationServeRefusesAndBecomesReadyOnce (A2) drives the real
// serve seam: bootstrap, authority, and socket refusals before any lease, and
// a Ready helper that records ready exactly once and ends on its context.
func TestClaudeRegistrationServeRefusesAndBecomesReadyOnce(t *testing.T) {
	t.Parallel()
	f := newClaudeEndpointTestFixture(t)
	idle := claudeEndpointIdleOptions{stat: (*intmetadata.Store).RegistryFileIdentity, now: time.Now, floor: claudeEndpointIdleRegistryFloor}
	for _, test := range []struct {
		name   string
		change func(*claudeEndpointBootstrap)
		want   diagnostics.ClaudeRegistrationReason
	}{
		{"no token", func(b *claudeEndpointBootstrap) { b.Token = "" }, diagnostics.ClaudeRegistrationBootstrapInvalid},
		{"relative registry", func(b *claudeEndpointBootstrap) { b.RegistryPath = "registry.json" }, diagnostics.ClaudeRegistrationBootstrapInvalid},
		{"no session", func(b *claudeEndpointBootstrap) { b.Registration.Authority.SessionID = "" }, diagnostics.ClaudeRegistrationAuthorityInvalid},
		{"absent socket", func(b *claudeEndpointBootstrap) { b.Socket = filepath.Join(f.root, "absent.sock") }, diagnostics.ClaudeRegistrationMessagingSocket},
	} {
		bootstrap := f.bootstrap
		test.change(&bootstrap)
		if got := serveClaudeRegistration(context.Background(), bootstrap, io.Discard, idle); got != test.want {
			t.Fatalf("%s: serve = %q, want %q", test.name, got, test.want)
		}
		if err := serveClaudeEndpointWithIdleGate(context.Background(), bootstrap, io.Discard, idle); claudeRegistrationRefusalReason(err) != test.want {
			t.Fatalf("%s: serve error = %v", test.name, err)
		}
	}

	cleanupClaudeAdmissionLeaseDir(t, f.bootstrap)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	readAck, writeAck := claudeHelperAckPipe(t)
	var ready sync.WaitGroup
	ready.Add(1)
	admissions := 0
	ended := make(chan diagnostics.ClaudeRegistrationReason, 1)
	idle.admitted = func() { admissions++; ready.Done() }
	go func() { ended <- serveClaudeRegistration(ctx, f.bootstrap, writeAck, idle) }()
	var ack [1]byte
	_ = readAck.SetReadDeadline(time.Now().Add(4 * time.Second))
	if _, err := io.ReadFull(readAck, ack[:]); err != nil || ack[0] != 1 {
		t.Fatalf("helper did not acknowledge: %v", err)
	}
	ready.Wait()
	cancel()
	select {
	case reason := <-ended:
		if reason != diagnostics.ClaudeRegistrationEndedContextDone || admissions != 1 {
			t.Fatalf("serve ended %q after %d admissions, want ended-context-done after one", reason, admissions)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("helper did not end on its context")
	}
}

// TestAdmitClaudeRegistrationClassifiesEveryClaimRefusal (A2) runs the claim
// transaction body on the Registry as each refusal leaves it.
func TestAdmitClaudeRegistrationClassifiesEveryClaimRefusal(t *testing.T) {
	t.Parallel()
	f := newClaudeEndpointTestFixture(t)
	base, err := f.store.LoadDegradedReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	mutator := intmetadata.DefaultMutator()
	binding := func(reg *coremetadata.Registry) *coremetadata.ClaudeActivationBinding {
		pane, _ := reg.Pane(f.bootstrap.PaneUID)
		return pane.Status.Activation.Claude
	}
	for _, test := range []struct {
		name   string
		change func(*coremetadata.Registry, *claudeEndpointBootstrap)
		want   diagnostics.ClaudeRegistrationReason
	}{
		{"admitted", func(*coremetadata.Registry, *claudeEndpointBootstrap) {}, claudeRegistrationProceed},
		{"provider gone", func(_ *coremetadata.Registry, b *claudeEndpointBootstrap) {
			b.Registration.Authority.Process.Start += "-gone"
		},
			diagnostics.ClaudeRegistrationProviderProcessGone},
		{"activation replaced", func(_ *coremetadata.Registry, b *claudeEndpointBootstrap) { b.Generation = "other-generation" },
			diagnostics.ClaudeRegistrationClaimRefusedActivation},
		{"agent stopped", func(reg *coremetadata.Registry, _ *claudeEndpointBootstrap) {
			agent, _ := reg.Agent(f.bootstrap.AgentUID)
			agent.Status.Phase = coremetadata.PhaseOffline
		}, diagnostics.ClaudeRegistrationClaimRefusedActivation},
		{"newer generation", func(reg *coremetadata.Registry, _ *claudeEndpointBootstrap) {
			binding(reg).RegistrationGeneration = "newer-generation"
		},
			diagnostics.ClaudeRegistrationClaimRefusedNewer},
		{"same generation other session", func(reg *coremetadata.Registry, _ *claudeEndpointBootstrap) {
			binding(reg).RegistrationSessionID = "other-session"
		},
			diagnostics.ClaudeRegistrationClaimRefusedCompeting},
		{"competing lease", func(reg *coremetadata.Registry, _ *claudeEndpointBootstrap) {
			authority := f.bootstrap.Registration.Authority
			authority.LeaseProcess.Start += "-other"
			binding(reg).Registration = &coremetadata.ClaudeRegistration{Authority: authority, Ready: true}
		}, diagnostics.ClaudeRegistrationClaimRefusedCompeting},
	} {
		t.Run(test.name, func(t *testing.T) {
			reg := base.Clone()
			bootstrap := f.bootstrap
			test.change(&reg, &bootstrap)
			err := admitClaudeRegistration(&reg, bootstrap, mutator)
			if got := claudeRegistrationRefusalReason(err); got != test.want || (test.want == claudeRegistrationProceed) != (err == nil) {
				t.Fatalf("admit = %v (%q), want %q", err, got, test.want)
			}
			if test.want == claudeRegistrationProceed {
				if registration := binding(&reg).Registration; registration == nil || !registration.Ready {
					t.Fatal("admitted registration is not Ready")
				}
			}
		})
	}
}

// TestClaudeRegistrationTransactionReasonClassifiesStoreFailures (A2) pins the
// Store failure classification, including the one lock-acquire-failed
// fallback, and runs two real Store failures through it.
func TestClaudeRegistrationTransactionReasonClassifiesStoreFailures(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		err     error
		entered bool
		want    diagnostics.ClaudeRegistrationReason
	}{
		{"callback refusal", refuseClaudeRegistration(diagnostics.ClaudeRegistrationClaimRefusedNewer), true, diagnostics.ClaudeRegistrationClaimRefusedNewer},
		{"validation or write after the callback", errors.New("metadata: rename temp registry"), true, diagnostics.ClaudeRegistrationRegistryWriteFailed},
		{"lock timeout", fmt.Errorf("metadata: acquire lock: %w after 30s", intmetadata.ErrLockTimeout), false, diagnostics.ClaudeRegistrationLockTimeout},
		{"degraded", fmt.Errorf("metadata: %w (invalid)", intmetadata.ErrRegistryDegraded), false, diagnostics.ClaudeRegistrationRegistryDegraded},
		{"malformed", fmt.Errorf("%w x: y", intmetadata.ErrMalformedRegistry), false, diagnostics.ClaudeRegistrationRegistryDegraded},
		{"state lost", fmt.Errorf("metadata: %w", intmetadata.ErrRegistryStateLost), false, diagnostics.ClaudeRegistrationRegistryDegraded},
		{"permission", fmt.Errorf("metadata: %w", intmetadata.ErrRegistryPermission), false, diagnostics.ClaudeRegistrationRegistryDegraded},
		{"fallback", errors.New("metadata: acquire registry lock: bad file descriptor"), false, diagnostics.ClaudeRegistrationLockAcquireFailed},
	} {
		if got := claudeRegistrationTransactionReason(test.err, test.entered); got != test.want {
			t.Fatalf("%s: %q, want %q", test.name, got, test.want)
		}
	}

	store := intmetadata.NewStore(intmetadata.PathFor(filepath.Join(t.TempDir(), "state")))
	entered := false
	_, _, err := store.UpdateConvergent(func(*coremetadata.Registry) error {
		entered = true
		return refuseClaudeRegistration(diagnostics.ClaudeRegistrationProviderProcessGone)
	})
	if got := claudeRegistrationTransactionReason(err, entered); got != diagnostics.ClaudeRegistrationProviderProcessGone {
		t.Fatalf("callback refusal through the Store = %q (%v)", got, err)
	}
	if err := os.WriteFile(store.Path(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	entered = false
	_, _, err = store.UpdateConvergent(func(*coremetadata.Registry) error { entered = true; return nil })
	if got := claudeRegistrationTransactionReason(err, entered); got != diagnostics.ClaudeRegistrationRegistryDegraded {
		t.Fatalf("malformed Registry through the Store = %q (%v)", got, err)
	}
}

// TestClaudeEndpointHelperLockObserverRecordsItsCommand (A5) pins that the
// helper's slow Registry lock acquisitions carry its route as the command.
func TestClaudeEndpointHelperLockObserverRecordsItsCommand(t *testing.T) {
	t.Parallel()
	journal := diagnostics.NewStore(filepath.Join(t.TempDir(), "operations.jsonl"))
	lifecycle := diagnostics.NewLifecycleRecorder(journal, "claude-helper-run", "1.0.0", "tmux")
	registry := intmetadata.NewStore(intmetadata.PathFor(t.TempDir()))
	registry.SetClock(steppedRegistryClock(time.Second))
	registry.SetLockObserver(newRegistryLockObserver(lifecycle.RegistryLock(diagnostics.Classify([]string{"internal", claudeEndpointHelperRoute}))))
	if _, _, err := registry.UpdateConvergent(func(*coremetadata.Registry) error { return nil }); err != nil {
		t.Fatal(err)
	}
	events, err := journal.Read()
	if err != nil || len(events) != 1 {
		t.Fatalf("events = %+v (err %v)", events, err)
	}
	if event := events[0]; event.Event != "registry.lock.acquisition" || event.Command != "claude-endpoint-helper" || event.Subcommand != "" {
		t.Fatalf("lock record = %+v, want command claude-endpoint-helper", event)
	}
}
