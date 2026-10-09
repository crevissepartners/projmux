package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

const testInstallOwnerRevision = "0123456789abcdef0123456789abcdef01234567"

func installPreflightFixture(fileVersion int, haveRegistry bool, owners []installProcessOwner) *installPreflightCommand {
	return &installPreflightCommand{
		schemaVersion:       5,
		coordinationVersion: 5,
		registryVersion:     func() (int, bool, error) { return fileVersion, haveRegistry, nil },
		owners:              func() ([]installProcessOwner, error) { return owners, nil },
	}
}

// A schema-changing install with a live owner stops before publication and
// names the owners and how to stop them; without a live owner, or without a
// version change, it publishes.
func TestInstallPreflightStopsSchemaChangingInstallWithLiveOwners(t *testing.T) {
	t.Parallel()
	owners := []installProcessOwner{
		{AgentName: "reviewer", AgentUID: "agent-b", Provider: aiModeCodex, Revision: testInstallOwnerRevision, Coordination: 5},
		{AgentName: "portfolio", AgentUID: "agent-a", Provider: aiModeClaude},
	}
	var stderr bytes.Buffer
	err := installPreflightFixture(4, true, owners).Run(&stderr)
	var exit installPreflightExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("schema change with live owners = %v, want the preflight refusal", err)
	}
	out := stderr.String()
	for _, want := range []string{
		"install stopped before publication",
		"Registry schemaVersion 4 to 5",
		"2 live process owners depend on it",
		"reviewer (uid:agent-b)  codex  revision " + testInstallOwnerRevision + "  coordination 5",
		"portfolio (uid:agent-a)  claude  revision unknown  coordination unknown",
		"Binary and live config are unchanged",
		"`projmux delete agent <agent-ref>`",
		"`projmux agent resume <agent-ref>`",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("refusal lacks %q:\n%s", want, out)
		}
	}

	for _, tc := range []struct {
		name         string
		fileVersion  int
		haveRegistry bool
		owners       []installProcessOwner
	}{
		{name: "schema change without owners", fileVersion: 4, haveRegistry: true},
		{name: "same schema with owners", fileVersion: 5, haveRegistry: true, owners: owners},
		{name: "no registry", owners: owners},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			if err := installPreflightFixture(tc.fileVersion, tc.haveRegistry, tc.owners).Run(&stderr); err != nil {
				t.Fatalf("preflight = %v, want publication:\n%s", err, stderr.String())
			}
			if !strings.HasPrefix(stderr.String(), ">> install preflight: ") {
				t.Fatalf("pass line = %q", stderr.String())
			}
		})
	}
}

// A live Claude owner that reports another coordination version stops the
// install; an unknown version and a Codex owner's version do not.
func TestInstallPreflightStopsOnClaudeCoordinationChange(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	changed := []installProcessOwner{{AgentName: "worker", AgentUID: "agent-a", Provider: aiModeClaude, Revision: testInstallOwnerRevision, Coordination: 4}}
	if err := installPreflightFixture(5, true, changed).Run(&stderr); err == nil {
		t.Fatalf("coordination change passed:\n%s", stderr.String())
	}
	if out := stderr.String(); !strings.Contains(out, "the Claude coordination version to 5") || !strings.Contains(out, "coordination 4 (changes)") {
		t.Fatalf("coordination refusal:\n%s", out)
	}
	for name, owners := range map[string][]installProcessOwner{
		"unknown": {{AgentName: "worker", AgentUID: "agent-a", Provider: aiModeClaude}},
		"codex":   {{AgentName: "worker", AgentUID: "agent-a", Provider: aiModeCodex, Coordination: 4}},
	} {
		stderr.Reset()
		if err := installPreflightFixture(5, true, owners).Run(&stderr); err != nil {
			t.Fatalf("%s owner stopped the install: %v\n%s", name, err, stderr.String())
		}
	}
}

// When the owners cannot be listed, only a schema-changing install stops.
func TestInstallPreflightUnlistedOwnersStopOnlySchemaChange(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		fileVersion int
		wantStop    bool
	}{{fileVersion: 4, wantStop: true}, {fileVersion: 5}} {
		command := installPreflightFixture(tc.fileVersion, true, nil)
		command.owners = func() ([]installProcessOwner, error) { return nil, errors.New("registry locked") }
		var stderr bytes.Buffer
		if err := command.Run(&stderr); (err != nil) != tc.wantStop {
			t.Fatalf("file version %d: preflight = %v, want stop %v\n%s", tc.fileVersion, err, tc.wantStop, stderr.String())
		}
	}
}

func TestReadRegistryFileSchemaVersionReadsTheEnvelopeOnly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.json")
	if _, ok, err := readRegistryFileSchemaVersion(path); ok || err != nil {
		t.Fatalf("absent registry = %v %v, want none", ok, err)
	}
	if err := os.WriteFile(path, []byte(`{"apiVersion":"x","schemaVersion":3,"panes":[{"bogus":true}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if version, ok, err := readRegistryFileSchemaVersion(path); version != 3 || !ok || err != nil {
		t.Fatalf("registry version = %d %v %v, want 3", version, ok, err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readRegistryFileSchemaVersion(path); err == nil {
		t.Fatal("malformed registry read a version")
	}
}

// A live owner is the recorded host process of a current process activation.
// The kernel identity decides liveness; the owner's own answer adds its build.
func TestLiveInstallProcessOwnersFollowsRecordedHostIdentity(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../core/metadata/testdata/registry-v5-process.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var reg coremetadata.Registry
	if err := json.Unmarshal(data, &reg); err != nil {
		t.Fatal(err)
	}
	host := reg.Panes[1].Status.Activation.Process.HostProcess
	agent := reg.Agents[0]
	observed := func(coremetadata.Registry, coremetadata.Pane) (processHostObservation, bool) {
		return processHostObservation{Protocol: 1, Revision: testInstallOwnerRevision, Coordination: 5}, true
	}
	owners := liveInstallProcessOwners(reg, func(p coremetadata.ProcessIdentity) bool { return p == host }, observed)
	want := []installProcessOwner{{AgentName: agent.Metadata.Name, AgentUID: agent.Metadata.UID, Provider: agent.Spec.Provider, Revision: testInstallOwnerRevision, Coordination: 5}}
	if len(owners) != 1 || owners[0] != want[0] {
		t.Fatalf("live owners = %+v, want %+v", owners, want)
	}
	if got := liveInstallProcessOwners(reg, func(coremetadata.ProcessIdentity) bool { return false }, observed); len(got) != 0 {
		t.Fatalf("exited owner counted: %+v", got)
	}
	unanswered := liveInstallProcessOwners(reg, func(coremetadata.ProcessIdentity) bool { return true },
		func(coremetadata.Registry, coremetadata.Pane) (processHostObservation, bool) {
			return processHostObservation{}, false
		})
	if len(unanswered) != 1 || unanswered[0].Revision != "" || unanswered[0].Coordination != 0 {
		t.Fatalf("unanswered owner = %+v, want unknown revision and coordination", unanswered)
	}
	// A protocol-0 answer carries no version even when a field decodes.
	protocolZero := liveInstallProcessOwners(reg, func(coremetadata.ProcessIdentity) bool { return true },
		func(coremetadata.Registry, coremetadata.Pane) (processHostObservation, bool) {
			return processHostObservation{Coordination: 5, Revision: testInstallOwnerRevision}, true
		})
	if len(protocolZero) != 1 || protocolZero[0].Revision != "" || protocolZero[0].Coordination != 0 {
		t.Fatalf("protocol-0 owner = %+v, want unknown", protocolZero)
	}
}

// A live fixture owner reports its build and coordination version, and the
// census finds it from the Registry by its kernel identity.
func TestLiveInstallProcessOwnersReadsLiveFixtureOwner(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			stubProcessHostRevision(t, testProcessHostRevision)
			h := newProcessProtocolHost(t, provider)
			owners := liveInstallProcessOwners(h.registry, installProcessHostIsAlive,
				func(registry coremetadata.Registry, pane coremetadata.Pane) (processHostObservation, bool) {
					return observeProcessHostOwner(registry, h.path, pane)
				})
			if len(owners) != 1 || owners[0].AgentUID != h.binding.Agent || owners[0].Provider != provider ||
				owners[0].Revision != testProcessHostRevision || owners[0].Coordination != claudeCoordinationVersion {
				t.Fatalf("live owners = %+v", owners)
			}
		})
	}
}

// The residue census records the retained owners as a count and prints them,
// with their builds, on the terminal only.
func TestInstallResidueCensusRetainsProcessOwners(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := time.Date(2026, 10, 9, 3, 0, 0, 0, time.UTC)
	command := &installResidueCommand{
		now:      func() time.Time { return now },
		getenv:   func(string) string { return "make" },
		stateDir: func() (string, error) { return dir, nil },
		readVintage: func(time.Time) projmuxProcessVintage {
			return projmuxProcessVintage{Supported: true, Roles: []projmuxProcessRoleVintage{
				{Role: projmuxProcessRoleProcessHostHelper, Processes: 1, Current: 1},
			}}
		},
		readOwners: func() ([]installProcessOwner, error) {
			return []installProcessOwner{{AgentName: "reviewer", AgentUID: "agent-b", Provider: aiModeCodex, Revision: testInstallOwnerRevision}}, nil
		},
	}
	var stderr bytes.Buffer
	command.Run(&stderr)
	out := stderr.String()
	for _, want := range []string{
		">> 1 live process owner retained; this install does not stop or replace it",
		"reviewer (uid:agent-b)  codex  revision " + testInstallOwnerRevision + "  coordination unknown",
		"`projmux agent resume <agent-ref>`",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("census notice lacks %q:\n%s", want, out)
		}
	}
	ledger, err := os.ReadFile(filepath.Join(dir, installResidueLedgerFile))
	if err != nil {
		t.Fatal(err)
	}
	var record installResidueRecord
	if err := json.Unmarshal(bytes.TrimSpace(ledger), &record); err != nil {
		t.Fatal(err)
	}
	if record.ProcessOwners != 1 {
		t.Fatalf("ledger processOwners = %d, want 1", record.ProcessOwners)
	}
	for _, leaked := range []string{"agent-b", "reviewer", testInstallOwnerRevision} {
		if bytes.Contains(ledger, []byte(leaked)) {
			t.Fatalf("ledger recorded %q: %s", leaked, ledger)
		}
	}
}

// The supervisor helper of a process Agent is a named, retained role: it is
// counted, and no install pass ever targets it.
func TestProcessHostHelperIsRetainedNotReplaced(t *testing.T) {
	t.Parallel()
	const self = "/home/user/go/bin/projmux"
	argv := []string{self, "internal", "process-host-supervisor"}
	if role := projmuxProcessRole(argv); role != projmuxProcessRoleProcessHostHelper {
		t.Fatalf("helper role = %q", role)
	}
	if got := replacementRoleDisposition(projmuxProcessRoleProcessHostHelper); got != replacementDispositionReportOnly {
		t.Fatalf("helper disposition = %q, want report-only", got)
	}
	images := []codexProcessImage{{PID: 21, Exe: self + procDeletedSuffix, Cmdline: argv}}
	if targets := projectInstallReplacementTargets(self, 1, images, nil, nil); len(targets) != 0 {
		t.Fatalf("install pass targets the helper: %+v", targets)
	}
	vintage := projectProjmuxProcessVintage(self, 1, images, true)
	if len(vintage.Roles) != 1 || vintage.Roles[0].Role != projmuxProcessRoleProcessHostHelper || vintage.Roles[0].Replaced != 1 {
		t.Fatalf("helper census = %+v", vintage.Roles)
	}
}
