package app

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// These diagnostics belong only to the named authority-failure consumers. They
// never wrap the shared fixture, proof, wait, or route implementation. Nothing
// below reads provider frames, credentials, argv, environment, or raw errors.
// The original assertion remains the failure evidence. Re-reading a socket or
// registration later cannot recover the original discover/lookup/route callback
// result (including its registration-equality decision).
type claudeAuthorityFailureDiagnostics struct {
	t               *testing.T
	started         time.Time
	beforeCaptured  bool
	failedAt        time.Time // entry to the failure defer, not the original error time
	paths           []string
	units           []*processClaudeFixture
	previous        []coremetadata.ProcessActivation
	previousHelpers []coremetadata.ProcessIdentity
	previousSockets []string
}

func newClaudeAuthorityFailureDiagnostics(t *testing.T) *claudeAuthorityFailureDiagnostics {
	d := &claudeAuthorityFailureDiagnostics{t: t, started: time.Now()}
	// Callers register this before their cleanup actions. No Stop or Wait is
	// added: the post-cleanup observation reports the existing cleanup's evidence.
	t.Cleanup(func() { d.capture("after-cleanup") })
	return d
}

func (d *claudeAuthorityFailureDiagnostics) beforeCleanup() { d.capture("before-cleanup") }

type claudeAuthorityProcessDiagnostic struct {
	PID                  int
	ExpectedBirthPresent bool
	ObservedBirth        string `json:",omitempty"`
	Read                 string
	BirthMatches         *bool `json:",omitempty"`
}

func claudeAuthorityProcessDiagnosticFor(expected coremetadata.ProcessIdentity) claudeAuthorityProcessDiagnostic {
	row := claudeAuthorityProcessDiagnostic{PID: expected.PID, ExpectedBirthPresent: expected.Start != "", Read: "not-recorded"}
	if expected.PID <= 0 {
		return row
	}
	actual, _, err := localipc.Process(expected.PID)
	row.Read = claudeAuthorityReadResult(err)
	if err == nil {
		row.ObservedBirth = actual.Start
		same := actual == expected
		row.BirthMatches = &same
	}
	return row
}

// A failed read is not evidence of absence or a stale identity.
func claudeAuthorityReadResult(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, os.ErrNotExist), errors.Is(err, localipc.ErrProcessAbsent):
		return "absent"
	case errors.Is(err, os.ErrPermission):
		return "permission-error"
	default:
		return "read-error"
	}
}

type claudeAuthorityPaneDiagnostic struct {
	Index                                                                                   int
	ActivationPresent, SessionRecordPresent, SessionPresent, ConnectionPresent, TurnPresent bool
	CurrentActivation, ActualWait                                                           bool
	WaitExitCode                                                                            *int `json:",omitempty"`
	WaitSignalPresent                                                                       bool
	WaitObservedAt                                                                          time.Time `json:",omitzero"`
	Host, Provider, Helper                                                                  claudeAuthorityProcessDiagnostic
	SocketStat, SocketInspect                                                               string
	SocketIsSocket                                                                          *bool `json:",omitempty"`
	RegistrationRead                                                                        string
	LookupReturnedStaleError                                                                *bool `json:",omitempty"`
	RegistrationPresent, RegistrationReady                                                  *bool `json:",omitempty"`
	ClaimRead                                                                               string
	ClaimPresent                                                                            *bool `json:",omitempty"`
	ClaimProcess                                                                            claudeAuthorityProcessDiagnostic
}

type claudeAuthorityUnitDiagnostic struct {
	Index                                                             int
	Observe                                                           string
	Ready, SessionPresent, ConnectionPresent, TurnPresent, ActualWait bool
	Pending                                                           int
	ExitSignal                                                        string `json:",omitempty"`
	ExitCode                                                          *int   `json:",omitempty"`
	CleanupDiagnosticPresent                                          bool
}

type claudeAuthorityDiagnostic struct {
	Phase                                                 string
	FailureDeferAt, SnapshotStartedAt, SnapshotFinishedAt time.Time
	Elapsed                                               time.Duration
	RegistryStats                                         []string
	RegistryReads                                         []string
	Panes                                                 []claudeAuthorityPaneDiagnostic
	Units                                                 []claudeAuthorityUnitDiagnostic
	PreviousProcesses                                     []claudeAuthorityProcessDiagnostic
	PreviousSocketStats                                   []string
}

func (d *claudeAuthorityFailureDiagnostics) capture(phase string) {
	if !d.t.Failed() || (len(d.paths) == 0 && len(d.units) == 0) {
		return
	}
	if phase == "before-cleanup" {
		if d.beforeCaptured {
			return
		}
		d.beforeCaptured = true
	}
	if d.failedAt.IsZero() {
		d.failedAt = time.Now()
	}
	row := claudeAuthorityDiagnostic{Phase: phase, FailureDeferAt: d.failedAt, SnapshotStartedAt: time.Now()}
	for _, path := range d.paths {
		_, statErr := os.Lstat(path)
		row.RegistryStats = append(row.RegistryStats, claudeAuthorityReadResult(statErr))
		reg, err := intmetadata.NewStore(path).LoadDegradedReadOnly()
		row.RegistryReads = append(row.RegistryReads, claudeAuthorityReadResult(err))
		if err != nil {
			continue
		}
		for i := range reg.Agents {
			agent := &reg.Agents[i]
			if agent.Spec.Provider != "claude" {
				continue
			}
			pane, ok := reg.Pane(agent.Status.PaneRef)
			if !ok {
				continue
			}
			p := claudeAuthorityPaneDiagnostic{Index: len(row.Panes), RegistrationRead: "not-attempted", SocketStat: "not-attempted", SocketInspect: "not-attempted"}
			p.Host.Read, p.Provider.Read, p.Helper.Read = "not-recorded", "not-recorded", "not-recorded"
			p.ActivationPresent = pane.Status.Activation.Process != nil
			p.SessionRecordPresent = pane.Status.ProcessSession != nil
			if session := pane.Status.ProcessSession; session != nil {
				p.SessionPresent = session.SessionID != ""
				p.ConnectionPresent, p.TurnPresent = session.ConnectionID != "", session.TurnID != ""
				p.ActualWait = coremetadata.MatchesProcessWait(session.Binding, pane.Status.LastTermination)
				if p.ActualWait {
					p.WaitExitCode = pane.Status.LastTermination.ExitCode
					p.WaitSignalPresent = pane.Status.LastTermination.Signal != ""
					p.WaitObservedAt = pane.Status.LastTermination.ObservedAt
				}
			}
			c := &agentCommand{messagePaths: agentMessagePaths{registryPath: path}}
			claim, claimErr := readDeferredClaim(c.deferredClaimPath(agent.Metadata.UID))
			p.ClaimRead = claudeAuthorityReadResult(claimErr)
			p.ClaimProcess.Read = "not-recorded"
			if claimErr == nil {
				present := claim.Agent != ""
				p.ClaimPresent = &present
				p.ClaimProcess = claudeAuthorityProcessDiagnosticFor(claim.Process)
			}
			if activation := pane.Status.Activation.Process; activation != nil {
				if phase == "before-cleanup" {
					d.previous = append(d.previous, *activation)
				}
				_, provider, valid := reg.CurrentProcessActivation(activation.Binding)
				p.CurrentActivation = valid && provider == "claude"
				p.Host = claudeAuthorityProcessDiagnosticFor(activation.HostProcess)
				p.Provider = claudeAuthorityProcessDiagnosticFor(activation.Child)
				socket := processClaudeHostSocket(path, pane.Metadata.UID, activation.Binding.Generation)
				if phase == "before-cleanup" {
					d.previousSockets = append(d.previousSockets, socket)
				}
				info, statErr := os.Lstat(socket)
				p.SocketStat = claudeAuthorityReadResult(statErr)
				if statErr == nil {
					isSocket := info.Mode()&os.ModeSocket != 0
					p.SocketIsSocket = &isSocket
				}
				identity, inspectErr := localipc.InspectOwnedSocket(socket)
				p.SocketInspect = claudeAuthorityReadResult(inspectErr)
				// One read-only registration snapshot, only after failure and before
				// cleanup. It is not a replay of discovery or either route lookup.
				if phase == "before-cleanup" && inspectErr == nil && pane.Status.ProcessSession != nil {
					proof := claudeProcessProof{Binding: processSchemaBinding(activation.Binding), Process: activation.Child, HostProcess: activation.HostProcess, Socket: socket, HostSocket: identity, Session: pane.Status.ProcessSession.SessionID}
					// Preserve an admitted nil/unready registration as observed data;
					// lookupClaudeProcessRegistration folds both into ErrStale.
					// The request remains the same read-only lookup operation.
					result, lookupErr := exchangeClaudeProcessHost(proof, claudeProcessCheck{Binding: proof.Binding, Session: proof.Session, Lookup: true})
					registration := result.Registration
					p.RegistrationRead = claudeAuthorityReadResult(lookupErr)
					stale := errors.Is(lookupErr, processhost.ErrStale)
					p.LookupReturnedStaleError = &stale
					if lookupErr == nil {
						present := registration != nil
						p.RegistrationPresent = &present
						if registration != nil {
							ready := registration.Ready
							p.RegistrationReady = &ready
							p.Helper = claudeAuthorityProcessDiagnosticFor(registration.Authority.LeaseProcess)
							d.previousHelpers = append(d.previousHelpers, registration.Authority.LeaseProcess)
						}
					}
				}
			}
			row.Panes = append(row.Panes, p)
		}
	}
	for i, f := range d.units {
		s, err := f.handle.Observe(f.binding)
		u := claudeAuthorityUnitDiagnostic{Index: i, Observe: claudeAuthorityReadResult(err)}
		if err == nil {
			u.Ready, u.SessionPresent, u.ConnectionPresent, u.TurnPresent = s.State == "ready", s.Session != "", s.Connection != "", s.Turn != ""
			u.Pending, u.ActualWait, u.CleanupDiagnosticPresent = len(s.Pending), s.Exit != nil, s.Diagnostic != ""
			if s.Exit != nil {
				code := s.Exit.Code
				u.ExitCode = &code
				u.ExitSignal = s.Exit.Signal
			}
		}
		row.Units = append(row.Units, u)
	}
	if phase == "after-cleanup" {
		for _, helper := range d.previousHelpers {
			row.PreviousProcesses = append(row.PreviousProcesses, claudeAuthorityProcessDiagnosticFor(helper))
		}
		for _, socket := range d.previousSockets {
			_, err := os.Lstat(socket)
			row.PreviousSocketStats = append(row.PreviousSocketStats, claudeAuthorityReadResult(err))
		}
		for _, activation := range d.previous {
			row.PreviousProcesses = append(row.PreviousProcesses, claudeAuthorityProcessDiagnosticFor(activation.HostProcess), claudeAuthorityProcessDiagnosticFor(activation.Child))
		}
	}
	row.SnapshotFinishedAt = time.Now()
	row.Elapsed = row.SnapshotFinishedAt.Sub(d.started)
	raw, err := json.Marshal(row)
	if err != nil {
		d.t.Log("Claude authority diagnostic encoding failed")
		return
	}
	d.t.Logf("Claude authority failure snapshot (later observation; FailureDeferAt is not original error time; original assertion above): %s", raw)
}

// Run a real testing.T failure in a subprocess so Fatal/Goexit executes the
// deferred snapshot and cleanup, while the outer redaction check can pass.
func TestClaudeAuthorityFailureDiagnostics(t *testing.T) {
	const modeKey = "PMX_TEST_AUTHORITY_DIAGNOSTIC_MODE"
	const secret = "authority-diagnostic-SECRET-prompt-token-session"
	if mode := os.Getenv(modeKey); mode != "" {
		if mode == "active-failure" {
			d := newClaudeAuthorityFailureDiagnostics(t)
			defer d.beforeCleanup()
			f := newProcessClaudeFixture(t, nil)
			d.units, d.paths = []*processClaudeFixture{f}, []string{f.path}
			_ = f.proof(t)
			if _, err := f.store.Update(func(reg *coremetadata.Registry) error {
				agent, _ := reg.Agent(f.binding.Agent)
				pane, _ := reg.Pane(f.binding.Pane)
				agent.Metadata.Labels = map[string]string{"private": secret}
				pane.Metadata.Labels = map[string]string{"private": secret}
				return nil
			}); err != nil {
				t.Fatal("prepare private sentinel")
			}
			t.Fatal("forced authority-consumer failure")
		}
		root := t.TempDir()
		path := filepath.Join(root, "registry.json")
		h := newSessionRefHarness(t, "claude")
		h.registry.Agents[0].Metadata.Labels = map[string]string{"private": secret}
		h.registry.Panes[0].Metadata.Labels = map[string]string{"private": secret}
		raw, err := json.Marshal(h.registry)
		if err != nil {
			t.Fatal("prepare synthetic registry")
		}
		if mode == "read-error" {
			raw = []byte(secret)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal("write synthetic registry")
		}
		d := newClaudeAuthorityFailureDiagnostics(t)
		d.paths = []string{path}
		defer d.beforeCleanup()
		if mode != "success" {
			t.Fatal("forced authority-consumer failure")
		}
		return
	}
	for _, mode := range []string{"success", "failure", "read-error", "active-failure"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestClaudeAuthorityFailureDiagnostics$", "-test.v")
			cmd.Env = append(os.Environ(), modeKey+"="+mode)
			out, err := cmd.CombinedOutput()
			text := string(out)
			if strings.Contains(text, secret) {
				t.Fatal("diagnostic exposed synthetic secret")
			}
			if mode == "success" {
				if err != nil || strings.Contains(text, "Claude authority failure snapshot") {
					t.Fatal("success path emitted diagnostics or failed")
				}
				return
			}
			if err == nil {
				t.Fatal("forced failure unexpectedly passed")
			}
			for _, want := range []string{"forced authority-consumer failure", `"Phase":"before-cleanup"`, `"Phase":"after-cleanup"`, `"FailureDeferAt":`, `"SnapshotStartedAt":`, `"SnapshotFinishedAt":`} {
				if !strings.Contains(text, want) {
					t.Fatalf("missing diagnostic %s: %s", want, text)
				}
			}
			var snapshots []claudeAuthorityDiagnostic
			for line := range strings.SplitSeq(text, "\n") {
				at := strings.Index(line, `{"Phase":`)
				if at < 0 {
					continue
				}
				var snapshot claudeAuthorityDiagnostic
				if err := json.Unmarshal([]byte(line[at:]), &snapshot); err != nil {
					t.Fatal("invalid diagnostic JSON")
				}
				snapshots = append(snapshots, snapshot)
			}
			if len(snapshots) != 2 {
				t.Fatalf("snapshot count %d, want 2", len(snapshots))
			}
			before, after := snapshots[0], snapshots[1]
			if before.Phase != "before-cleanup" || after.Phase != "after-cleanup" || before.FailureDeferAt.IsZero() || !before.FailureDeferAt.Equal(after.FailureDeferAt) || before.SnapshotStartedAt.Before(before.FailureDeferAt) || before.SnapshotFinishedAt.Before(before.SnapshotStartedAt) || after.SnapshotStartedAt.Before(before.SnapshotFinishedAt) || after.SnapshotFinishedAt.Before(after.SnapshotStartedAt) {
				t.Fatal("failure-defer/snapshot timing or cleanup order lost")
			}
			if mode == "active-failure" {
				if len(before.Units) != 1 || before.Units[0].ActualWait || len(after.Units) != 1 || !after.Units[0].ActualWait {
					t.Fatal("existing fixture Wait not distinguished before/after cleanup")
				}
				if len(before.Panes) != 1 || before.Panes[0].RegistrationRead != "ok" || before.Panes[0].RegistrationReady == nil || !*before.Panes[0].RegistrationReady || before.Panes[0].Helper.Read != "ok" {
					t.Fatal("live fixture registration/helper snapshot missing")
				}
			}
			if mode == "read-error" && !strings.Contains(text, `"RegistryReads":["read-error"]`) {
				t.Fatalf("read error not preserved: %s", text)
			}
			if mode == "failure" && !strings.Contains(text, `"ActivationPresent":false`) {
				t.Fatalf("synthetic pane not observed: %s", text)
			}
		})
	}
}

func TestClaudeAuthorityDiagnosticReadStates(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, "ok"}, {os.ErrNotExist, "absent"}, {localipc.ErrProcessAbsent, "absent"},
		{os.ErrPermission, "permission-error"}, {errors.New("sensitive raw read error"), "read-error"},
	} {
		if got := claudeAuthorityReadResult(tc.err); got != tc.want {
			t.Fatalf("read state %s, want %s", got, tc.want)
		}
	}
	expected, _, err := localipc.Process(os.Getpid())
	if err != nil {
		t.Fatal("cannot observe own birth identity")
	}
	row := claudeAuthorityProcessDiagnosticFor(expected)
	if row.Read != "ok" || row.BirthMatches == nil || !*row.BirthMatches || row.ObservedBirth == "" {
		t.Fatal("own birth identity not retained")
	}
	expected.Start = "sensitive-untrusted-registry-birth"
	row = claudeAuthorityProcessDiagnosticFor(expected)
	raw, _ := json.Marshal(row)
	if row.BirthMatches == nil || *row.BirthMatches || strings.Contains(string(raw), expected.Start) {
		t.Fatal("birth mismatch lost or untrusted birth exposed")
	}
}
