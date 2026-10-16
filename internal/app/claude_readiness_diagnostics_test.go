package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// Last is the last *original* evaluation, not a replay during diagnostics.
// Iteration distinguishes retained facts from an operand skipped this time.
// Names below are source constants; no provider-controlled string is logged.
type claudeReadinessFact struct {
	Evaluated, CurrentIteration bool
	Last bool
	At time.Time
	Iteration int
}

type claudeReadinessTrace struct {
	Started time.Time
	Iteration int
	Facts map[string]claudeReadinessFact
	Returned map[string]claudeReadinessReturn
	Record *claudeReadinessRecord
	otherProvider bool
}

type claudeReadinessRecord struct {
	At time.Time
	Iteration int
	Session, Connection, Turn, Thread, Pending, Resumable bool
}

type claudeReadinessReturn struct {
	At time.Time
	Iteration int
	Read string
	Value *bool
}

func newClaudeReadinessTrace() *claudeReadinessTrace {
	d := &claudeReadinessTrace{Started: time.Now(), Facts: make(map[string]claudeReadinessFact), Returned: make(map[string]claudeReadinessReturn)}
	for _, name := range []string{"registry-read-ok", "pane-present", "session-record-present", "session-present", "connection-present", "turn-empty", "observe-ok", "host-ready", "proof-guard", "attention-guard", "attention-binding-matches", "attention-provider-claude", "attention-sequence-positive", "attention-response-complete", "registration-lookup-ok", "opaque-accept-callback", "source-ineligible", "target-ineligible"} {
		d.Facts[name] = claudeReadinessFact{}
	}
	return d
}

// Returned values are projections of already completed original calls. They do
// not claim that a later short-circuit guard evaluated those values. Discovery
// includes an internal registration lookup whose subpredicates remain opaque.
func (d *claudeReadinessTrace) result(name string, err error) {
	d.Returned[name] = claudeReadinessReturn{At: time.Now(), Iteration: d.Iteration, Read: claudeAuthorityReadResult(err)}
}

func (d *claudeReadinessTrace) resultBool(name string, value bool) {
	d.Returned[name] = claudeReadinessReturn{At: time.Now(), Iteration: d.Iteration, Value: &value}
}

func (d *claudeReadinessTrace) check(name string, value bool) bool {
	d.Facts[name] = claudeReadinessFact{true, true, value, time.Now(), d.Iteration}
	return value
}

// This is a projection of an existing record, not evidence that an opaque
// accept callback evaluated any particular member of that record.
func (d *claudeReadinessTrace) record(s *coremetadata.ProcessSessionRecord) {
	d.otherProvider = s.Provider != "claude"
	d.Record = &claudeReadinessRecord{time.Now(), d.Iteration, s.SessionID != "", s.ConnectionID != "", s.TurnID != "", s.ThreadID != "", len(s.Pending) > 0, s.ResumeState == coremetadata.ProcessResumable}
}

type claudeReadinessRoot struct {
	At time.Time
	Read string
	Device, Inode uint64
	Directory bool
}

func claudeReadinessRootFor(root string) claudeReadinessRoot {
	r := claudeReadinessRoot{At: time.Now()}
	info, err := os.Stat(root)
	r.Read = claudeAuthorityReadResult(err)
	if err == nil {
		r.Directory = info.IsDir()
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			r.Device, r.Inode = uint64(stat.Dev), uint64(stat.Ino)
		}
	}
	return r
}

type claudeReadinessProcess struct {
	At time.Time
	Process claudeAuthorityProcessDiagnostic
}

func claudeReadinessProcessFor(p coremetadata.ProcessIdentity) claudeReadinessProcess {
	r := claudeReadinessProcess{time.Now(), claudeAuthorityProcessDiagnosticFor(p)}
	// Only compare the birth token here. A CLI PID first learned from exec.Cmd
	// has no recorded expected birth; do not invent a mismatch from missing UID.
	r.Process.BirthMatches = nil
	if p.Start != "" && r.Process.Read == "ok" {
		matches := p.Start == r.Process.ObservedBirth
		r.Process.BirthMatches = &matches
	}
	return r
}

type claudeReadinessSnapshot struct {
	Started, Finished time.Time
	Root claudeReadinessRoot
	RegistryRead string
	Pane, SessionRecord, Activation bool
	Host, Provider, Claim claudeReadinessProcess
	SocketStat, ClaimRead string
	// The helper lives in host memory. No lookup/RPC is replayed to infer the
	// original discovery result; absence of a sample is not process absence.
	HelperRead string
	ActualProviderWait bool
	ProviderWaitAt time.Time
	ProviderExitCode *int
	ProviderSignal bool
}

func claudeReadinessSnapshotFor(f processCreateCLI, ref string) claudeReadinessSnapshot {
	r := claudeReadinessSnapshot{Started: time.Now(), Root: claudeReadinessRootFor(f.root), SocketStat: "not-observed", ClaimRead: "not-observed", HelperRead: "not-observed-host-memory"}
	r.Host = claudeReadinessProcessFor(coremetadata.ProcessIdentity{})
	r.Provider = claudeReadinessProcessFor(coremetadata.ProcessIdentity{})
	r.Claim = claudeReadinessProcessFor(coremetadata.ProcessIdentity{})
	reg, err := f.store.LoadDegradedReadOnly()
	r.RegistryRead = claudeAuthorityReadResult(err)
	if err == nil {
		uid := strings.TrimPrefix(ref, "uid:")
		pane, _ := processResumePane(reg, uid)
		r.Pane = pane != nil
		if pane != nil {
			r.SessionRecord = pane.Status.ProcessSession != nil
			r.Activation = pane.Status.Activation.Process != nil
			if a := pane.Status.Activation.Process; a != nil {
				r.Host, r.Provider = claudeReadinessProcessFor(a.HostProcess), claudeReadinessProcessFor(a.Child)
				_, statErr := os.Lstat(processClaudeHostSocket(f.store.Path(), pane.Metadata.UID, a.Binding.Generation))
				r.SocketStat = claudeAuthorityReadResult(statErr)
			}
			if s := pane.Status.ProcessSession; s != nil && coremetadata.MatchesProcessWait(s.Binding, pane.Status.LastTermination) {
				r.ActualProviderWait = true
				r.ProviderWaitAt = pane.Status.LastTermination.ObservedAt
				r.ProviderExitCode = pane.Status.LastTermination.ExitCode
				r.ProviderSignal = pane.Status.LastTermination.Signal != ""
			}
		}
		c := deferredFixtureCommand(f)
		claim, claimErr := readDeferredClaim(c.deferredClaimPath(uid))
		r.ClaimRead = claudeAuthorityReadResult(claimErr)
		if claimErr == nil {
			r.Claim = claudeReadinessProcessFor(claim.Process)
		}
	}
	r.Finished = time.Now()
	return r
}

func claudeReadinessLog(t *testing.T, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Log("Claude readiness diagnostic encoding failed")
		return
	}
	t.Logf("Claude readiness diagnostic (original facts and later non-atomic snapshot; missing samples do not prove absence): %s", raw)
}

func (d *claudeReadinessTrace) failure(t *testing.T, f processCreateCLI, ref string) {
	t.Helper()
	if d.otherProvider { return }
	for name, fact := range d.Facts {
		fact.CurrentIteration = fact.Evaluated && fact.Iteration == d.Iteration
		d.Facts[name] = fact
	}
	claudeReadinessLog(t, struct {
		Kind string
		Original *claudeReadinessTrace
		Later claudeReadinessSnapshot
	}{"predicate", d, claudeReadinessSnapshotFor(f, ref)})
}

// Only the result of an existing call is recorded. These fields never cause
// Stop, Kill, Wait, Close or RemoveAll to be invoked a second time.
type claudeReadinessCall struct {
	Started, Finished time.Time
	Attempted bool
	Result string
	ExitCode *int
	Signal bool
}

func claudeReadinessCallResult(start time.Time, err error) claudeReadinessCall {
	r := claudeReadinessCall{Started: start, Finished: time.Now(), Attempted: true, Result: claudeAuthorityReadResult(err)}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code := exit.ExitCode()
		r.ExitCode = &code
		if status, ok := exit.Sys().(syscall.WaitStatus); ok {
			r.Signal = status.Signaled()
		}
	}
	return r
}

type claudeReadinessCleanup struct {
	Close, Kill, CLIWait []claudeReadinessCall
	Before, After claudeReadinessSnapshot
	CLI claudeReadinessProcess
	KnownAfter []claudeReadinessProcess
	ExternalRootRemove string
}

func (r *claudeReadinessCleanup) before(t *testing.T, f processCreateCLI, ref string, cmd *exec.Cmd) {
	if !t.Failed() { return }
	r.Before = claudeReadinessSnapshotFor(f, ref)
	r.CLI = claudeReadinessProcessFor(coremetadata.ProcessIdentity{PID: cmd.Process.Pid})
	r.ExternalRootRemove = "not-observed"
}

func (r *claudeReadinessCleanup) after(t *testing.T, f processCreateCLI, ref string) {
	if !t.Failed() { return }
	r.After = claudeReadinessSnapshotFor(f, ref)
	// Recheck the already observed kernel births even if activation was retired.
	for _, p := range []claudeReadinessProcess{r.Before.Host, r.Before.Provider, r.Before.Claim, r.CLI} {
		if p.Process.PID > 0 && p.Process.ObservedBirth != "" {
			r.KnownAfter = append(r.KnownAfter, claudeReadinessProcessFor(coremetadata.ProcessIdentity{PID: p.Process.PID, Start: p.Process.ObservedBirth}))
		}
	}
	claudeReadinessLog(t, struct { Kind string; Cleanup *claudeReadinessCleanup }{"cleanup-cli-wait-is-not-provider-wait", r})
}

func claudeReadinessRootRemoved(t *testing.T, before claudeReadinessRoot, start time.Time, err error, root string) {
	if !t.Failed() { return }
	result := claudeReadinessCallResult(start, err)
	claudeReadinessLog(t, struct {
		Kind string
		Before, After claudeReadinessRoot
		Remove claudeReadinessCall
	}{"owned-root-removal", before, claudeReadinessRootFor(root), result})
}

func TestClaudeReadinessPredicateDiagnostics(t *testing.T) {
	d := newClaudeReadinessTrace()
	d.Iteration++
	calls := 0
	operand := func() bool { calls++; return true }
	if d.check("session", false) && d.check("lookup", operand()) { t.Fatal("short circuit lost") }
	if calls != 0 { t.Fatal("skipped predicate was evaluated") }
	d.Iteration++
	if !d.check("session", true) || !d.check("lookup", operand()) { t.Fatal("stored value changed") }
	d.Iteration++
	d.check("session", false)
	if calls != 1 || d.Facts["session"].Iteration != 3 || d.Facts["lookup"].Iteration != 2 || !d.Facts["lookup"].Last || d.Facts["lookup"].At.IsZero() { t.Fatal("last evaluation or current mask lost") }
	_, present := d.Facts["attention"]
	if present { t.Fatal("unevaluated predicate invented") }
	secret := "readiness-private-error-session-provider-token"
	call := claudeReadinessCallResult(time.Now(), errors.New(secret))
	raw, err := json.Marshal(call)
	if err != nil || strings.Contains(string(raw), secret) || call.Result != "read-error" || !call.Attempted { t.Fatal("call error privacy/result lost") }
	birth, _, err := localipc.Process(os.Getpid())
	if err != nil { t.Fatal("own kernel birth unavailable") }
	birth.Start = secret
	row := claudeReadinessProcessFor(birth)
	raw, _ = json.Marshal(row)
	if strings.Contains(string(raw), secret) || row.Process.ObservedBirth == "" || row.Process.BirthMatches == nil || *row.Process.BirthMatches { t.Fatal("kernel birth confused with untrusted expected birth") }
}

// A real testing.T Fatal/Goexit exercises the original timeout, without needing
// a provider, a product binary, or a live Registry. Child output is checked whole.
func TestClaudeReadinessFailureDiagnostics(t *testing.T) {
	const key = "PMX_TEST_READINESS_DIAGNOSTIC_MODE"
	const secret = "readiness-PRIVATE-session-uid-prompt-error-token"
	if mode := os.Getenv(key); mode != "" {
		reg, uid, paneUID := processDeleteFixture(t, "claude")
		reg.Agents[0].Metadata.Labels = map[string]string{"private": secret}
		pane, _ := reg.Pane(paneUID)
		pane.Status.ProcessSession.SessionID = secret
		pane.Status.ProcessSession.ConnectionID = secret
		pane.Status.ProcessSession.TurnID = secret
		pane.Status.Activation.Process.HostProcess = coremetadata.ProcessIdentity{PID: os.Getpid(), OwnerUID: uint32(os.Getuid()), Start: secret}
		pane.Status.Activation.Process.Child = pane.Status.Activation.Process.HostProcess
		root := filepath.Join(t.TempDir(), secret)
		if os.Mkdir(root, 0700) != nil { t.Fatal("prepare private fixture root") }
		store := intmetadata.NewStore(filepath.Join(root, "registry.json"))
		raw, err := json.Marshal(reg)
		if err != nil || os.WriteFile(store.Path(), raw, 0600) != nil { t.Fatal("prepare synthetic registry") }
		f := processCreateCLI{root: root, store: store}
		ref := "uid:" + uid
		if mode == "cleanup-claim" || mode == "cleanup-invocation" || mode == "wait-before-failure" {
			binary := filepath.Join(root, "cli-fixture")
			ownership := "claimed"
			if mode != "cleanup-claim" { ownership = "owned" }
			script := "#!/bin/sh\nprintf 'agent uid:private pane uid:private runtime=process foreground=" + ownership + "\\n'\nread item\nexit 0\n"
			if os.WriteFile(binary, []byte(script), 0700) != nil { t.Fatal("prepare CLI-only fixture") }
			f.binary = binary
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if mode == "cleanup-claim" { startDeferredCLIClaim(t, ctx, f, ref) } else {
				run := startResumeCLIInvocation(t, ctx, f, nil)
				if mode == "wait-before-failure" { run.shutdown(t) }
			}
			t.Fatal("forced cleanup-consumer failure")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		switch mode {
		case "deferred": deferredReady(t, ctx, f, ref)
		case "await":
			calls := 0
			t.Cleanup(func() { if calls != 1 { t.Error("callback count changed") } else { t.Log("callback count preserved") } })
			awaitProcessResumeRecord(t, ctx, f, ref, func(*coremetadata.ProcessSessionRecord) bool { calls++; return false })
		case "missing": awaitProcessResumeRecord(t, ctx, f, "uid:missing", func(*coremetadata.ProcessSessionRecord) bool { t.Fatal("absent record callback executed"); return true })
		case "success": awaitProcessResumeRecord(t, ctx, f, ref, func(*coremetadata.ProcessSessionRecord) bool { return true })
		}
		return
	}
	for _, mode := range []string{"deferred", "await", "missing", "success", "cleanup-claim", "cleanup-invocation", "wait-before-failure"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestClaudeReadinessFailureDiagnostics$", "-test.v")
			cmd.Env = append(os.Environ(), key+"="+mode)
			out, err := cmd.CombinedOutput()
			text := string(out)
			if strings.Contains(text, secret) { t.Fatal("private sentinel exposed") }
			if mode == "success" {
				if err != nil || strings.Contains(text, "Claude readiness diagnostic") { t.Fatal("success failed or emitted diagnostics") }
				return
			}
			if strings.HasPrefix(mode, "cleanup-") || mode == "wait-before-failure" {
				if err == nil || !strings.Contains(text, `"Kind":"cleanup-cli-wait-is-not-provider-wait"`) || !strings.Contains(text, `"CLIWait":`) || !strings.Contains(text, `"Attempted":true`) || !strings.Contains(text, `"ExternalRootRemove":"not-observed"`) { t.Fatal("original CLI Wait cleanup evidence absent") }
				return
			}
			if err == nil || !strings.Contains(text, "Claude readiness diagnostic") || !strings.Contains(text, `"Kind":"predicate"`) { t.Fatal("forced timeout diagnostic absent") }
			original := "process session predicate timed out"
			if mode == "deferred" { original = "verified provider init/endpoint not ready" }
			if !strings.Contains(text, original) { t.Fatal("original timeout changed") }
			var trace claudeReadinessTrace
			for line := range strings.SplitSeq(text, "\n") {
				at := strings.Index(line, `{"Kind":"predicate"`)
				if at < 0 { continue }
				var record struct { Original claudeReadinessTrace; Later claudeReadinessSnapshot }
				if json.Unmarshal([]byte(line[at:]), &record) != nil { t.Fatal("invalid diagnostic JSON") }
				trace = record.Original
				if record.Later.Started.IsZero() || record.Later.Finished.Before(record.Later.Started) || record.Later.Root.Inode == 0 { t.Fatal("snapshot timing/root identity lost") }
			}
			if trace.Iteration != 1 { t.Fatal("forced timeout did not preserve one original iteration") }
			switch mode {
			case "deferred":
				if trace.Facts["turn-empty"].Last || trace.Facts["turn-empty"].Iteration != 1 || trace.Facts["host-ready"].Iteration != 0 { t.Fatal("deferred short circuit evidence lost") }
			case "await":
				if trace.Facts["opaque-accept-callback"].Last || trace.Facts["opaque-accept-callback"].Iteration != 1 || !strings.Contains(text, "callback count preserved") || strings.Contains(text, "callback count changed") { t.Fatal("opaque callback count/outcome lost") }
			case "missing":
				if trace.Facts["pane-present"].Last || trace.Facts["opaque-accept-callback"].Iteration != 0 || strings.Contains(text, "absent record callback executed") { t.Fatal("missing pane evaluated callback") }
			}
		})
	}
}
