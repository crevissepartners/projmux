package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/core/selector"
)

// auditMutator is a deterministic Mutator over a fixed set of existing roots.
// It mirrors pbtMutator, which is single-root, because the audit fixture has to
// hold more than one Project at once.
func auditMutator(roots ...string) coremetadata.Mutator {
	counters := map[coremetadata.Kind]int{}
	clock := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	return coremetadata.Mutator{
		Now: func() time.Time {
			clock = clock.Add(time.Second)
			return clock
		},
		NewUID: func(kind coremetadata.Kind) (string, error) {
			counters[kind]++
			return fmt.Sprintf("%s-audit%03d", strings.ToLower(string(kind)), counters[kind]), nil
		},
		DirExists: func(path string) (bool, error) {
			return slices.Contains(roots, strings.TrimSpace(path)), nil
		},
	}
}

func auditRegisterProject(t *testing.T, registry *coremetadata.Registry, mutator coremetadata.Mutator, root, session string) coremetadata.Project {
	t.Helper()
	result, err := mutator.RegisterProject(registry, coremetadata.RegisterProjectOptions{Root: root, OperationID: "audit-register"})
	if err != nil {
		t.Fatalf("register project %s: %v", root, err)
	}
	if _, err := mutator.BindProjectSession(registry, result.Project.Metadata.UID, session, false); err != nil {
		t.Fatalf("bind project session %s: %v", session, err)
	}
	project, ok := registry.Project(result.Project.Metadata.UID)
	if !ok {
		t.Fatalf("registered Project %s disappeared", root)
	}
	return *project
}

// auditDecayedWindow proves a current-schema delete cannot reproduce the
// 2026-08-20 field structure: deleting the last shell installs a new bare
// canonical shell and keeps the Window materializable.
func auditDecayedWindow(t *testing.T, registry *coremetadata.Registry, mutator coremetadata.Mutator, project coremetadata.Project, agents int) coremetadata.Window {
	t.Helper()
	window := registry.WindowsOf(project.Metadata.UID)[0]
	shell := registry.PanesOf(window.Metadata.UID)[0]
	for i := range agents {
		agent, err := mutator.CreateAgent(registry, window.Metadata.UID, coremetadata.CreateAgentOptions{
			Provider:    "codex",
			OperationID: fmt.Sprintf("audit-agent-%d", i),
		})
		if err != nil {
			t.Fatalf("create agent %d: %v", i, err)
		}
		if _, err := mutator.AttachAgentPane(registry, agent.Metadata.UID, coremetadata.BootstrapPane{}, fmt.Sprintf("audit-attach-%d", i)); err != nil {
			t.Fatalf("attach agent pane %d: %v", i, err)
		}
		if _, err := mutator.ReleaseAgentPane(registry, agent.Metadata.UID, coremetadata.AgentExitNormal, "audit-release"); err != nil {
			t.Fatalf("release agent pane %d: %v", i, err)
		}
	}
	if err := mutator.DeletePane(registry, shell.Metadata.UID); err != nil {
		t.Fatalf("delete shell pane: %v", err)
	}
	stored, ok := registry.Window(window.Metadata.UID)
	if !ok {
		t.Fatalf("decayed Window disappeared")
	}
	if strings.TrimSpace(stored.Spec.PrimaryPaneRef) == "" {
		t.Fatal("last-shell deletion left primaryPaneRef empty")
	}
	if panes := registry.PanesOf(window.Metadata.UID); len(panes) != 1 || panes[0].Metadata.UID != stored.Spec.PrimaryPaneRef || panes[0].Spec.Role != coremetadata.PaneRoleShell {
		t.Fatalf("replacement shell chain = %+v", panes)
	}
	return *stored
}

// auditFixture carries one repaired Project and one Project whose root is gone,
// so only the runtime root condition remains a materialize refusal.
func auditFixture(t *testing.T) (coremetadata.Registry, string) {
	t.Helper()
	live := t.TempDir()
	gone := t.TempDir()
	mutator := auditMutator(live, gone)
	registry := coremetadata.NewRegistry()

	project := auditRegisterProject(t, &registry, mutator, live, "audit-live")
	auditDecayedWindow(t, &registry, mutator, project, 2)
	if _, _, err := mutator.AddWindow(&registry, project.Metadata.UID, coremetadata.BootstrapWindow{Name: "healthy"}, "sh", "audit-window"); err != nil {
		t.Fatalf("add healthy window: %v", err)
	}
	auditRegisterProject(t, &registry, mutator, gone, "audit-gone")
	if err := os.RemoveAll(gone); err != nil {
		t.Fatalf("remove project root: %v", err)
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("audit fixture the product wrote is invalid: %v", err)
	}
	return registry, gone
}

func auditHealthyFixture(t *testing.T) coremetadata.Registry {
	t.Helper()
	root := t.TempDir()
	mutator := auditMutator(root)
	registry := coremetadata.NewRegistry()
	project := auditRegisterProject(t, &registry, mutator, root, "audit-healthy")
	if _, _, err := mutator.AddWindow(&registry, project.Metadata.UID, coremetadata.BootstrapWindow{Name: "second"}, "sh", "audit-window"); err != nil {
		t.Fatalf("add window: %v", err)
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("healthy fixture is invalid: %v", err)
	}
	return registry
}

func auditDoctor(t *testing.T, registry coremetadata.Registry) *doctorCommand {
	t.Helper()
	cmd := newStubDoctorCommand("linux", map[string]bool{"tmux": true, "git": true, "stty": true})
	cmd.readRegistry = func() (coremetadata.Registry, error) { return registry, nil }
	return cmd
}

func auditFinding(findings []doctorFinding, code string) (doctorFinding, bool) {
	for _, finding := range findings {
		if finding.Code == code {
			return finding, true
		}
	}
	return doctorFinding{}, false
}

// TestDoctorRegistryInvariantAuditReportsTheFieldFailureStructure is acceptance
// 1: the real 2026-08-20 structure has to reach the operator as a count, a
// resource kind, and a stated reason instead of passing diagnostics as healthy.
func TestDoctorRegistryInvariantAuditReportsTheFieldFailureStructure(t *testing.T) {
	registry, goneRoot := auditFixture(t)
	cmd := auditDoctor(t, registry)

	findings := cmd.evaluateRegistryInvariants()
	audited, ok := auditFinding(findings, doctorRegistryCodeAudited)
	if !ok || audited.Count != 2 {
		t.Fatalf("audited finding = %#v, want both Projects planned", audited)
	}
	if _, ok := auditFinding(findings, doctorRegistryCodeClean); ok {
		t.Fatalf("divergent Registry reported clean: %#v", findings)
	}
	if window, ok := auditFinding(findings, doctorRegistryRefusalCode("skipped", "window")); ok {
		t.Fatalf("repaired Window still produced a skip finding: %#v", window)
	}
	project, ok := auditFinding(findings, doctorRegistryRefusalCode("fatal", "project"))
	if !ok || project.Count != 1 || project.Severity != doctorSeverityError {
		t.Fatalf("project finding = %#v, want one fatal Project refusal", project)
	}

	var text bytes.Buffer
	if err := cmd.Run([]string{"--section", "registry"}, &text, io.Discard); err != nil {
		t.Fatalf("Run(--section registry) error = %v", err)
	}
	for _, want := range []string{
		"Registry materialization invariants",
		"registry.materialize.fatal.project; count: 1",
		"remediation: " + doctorRemediationInspectRegistryTopology,
	} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("registry text missing %q:\n%s", want, text.String())
		}
	}
	// Path-bearing reasons follow the report's established --verbose boundary.
	if strings.Contains(text.String(), goneRoot) {
		t.Fatalf("non-verbose registry text leaked a stored path:\n%s", text.String())
	}
	var verbose bytes.Buffer
	if err := cmd.Run([]string{"--section", "registry", "--verbose"}, &verbose, io.Discard); err != nil {
		t.Fatalf("Run(--section registry --verbose) error = %v", err)
	}
	if !strings.Contains(verbose.String(), "reason: ") || !strings.Contains(verbose.String(), goneRoot) {
		t.Fatalf("verbose registry text omitted the stated reasons:\n%s", verbose.String())
	}
}

// TestDoctorRegistryInvariantAuditStatesTheZeroCase is acceptance 2. A clean
// audit reports that it is clean; silence would be indistinguishable from a
// section that never ran.
func TestDoctorRegistryInvariantAuditStatesTheZeroCase(t *testing.T) {
	cmd := auditDoctor(t, auditHealthyFixture(t))

	findings := cmd.evaluateRegistryInvariants()
	clean, ok := auditFinding(findings, doctorRegistryCodeClean)
	if !ok || clean.Severity != doctorSeverityInfo {
		t.Fatalf("healthy Registry findings = %#v, want an explicit clean result", findings)
	}
	audited, ok := auditFinding(findings, doctorRegistryCodeAudited)
	if !ok || audited.Count != 1 {
		t.Fatalf("audited finding = %#v, want the one Project planned", audited)
	}
	for _, finding := range findings {
		if finding.Severity != doctorSeverityInfo {
			t.Fatalf("healthy Registry produced %#v", finding)
		}
	}

	// The clean line is printed without --verbose on purpose.
	var text bytes.Buffer
	if err := cmd.Run([]string{"--section", "registry"}, &text, io.Discard); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, want := range []string{doctorRegistryCodeClean, doctorRegistryCodeAudited + "; count: 1"} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("clean registry text missing %q:\n%s", want, text.String())
		}
	}
}

// TestDoctorRegistryInvariantAuditGoldens pins the zero and N section output in
// both formats.
func TestDoctorRegistryInvariantAuditGoldens(t *testing.T) {
	divergent, _ := auditFixture(t)
	for _, tc := range []struct {
		name     string
		registry coremetadata.Registry
		args     []string
		fixture  string
	}{
		{name: "clean-text", registry: auditHealthyFixture(t), args: []string{"--section", "registry"}, fixture: "testdata/doctor/registry-clean.golden"},
		{name: "clean-json", registry: auditHealthyFixture(t), args: []string{"--section", "registry", "--json"}, fixture: "testdata/doctor/registry-clean.golden.json"},
		{name: "divergent-text", registry: divergent, args: []string{"--section", "registry"}, fixture: "testdata/doctor/registry-divergent.golden"},
		{name: "divergent-json", registry: divergent, args: []string{"--section", "registry", "--json"}, fixture: "testdata/doctor/registry-divergent.golden.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := auditDoctor(t, tc.registry)
			var stdout bytes.Buffer
			if err := cmd.Run(tc.args, &stdout, io.Discard); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			want, err := os.ReadFile(tc.fixture)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(stdout.Bytes(), want) {
				t.Fatalf("golden drift\ngot:\n%s\nwant:\n%s", stdout.String(), want)
			}
		})
	}
}

// TestDoctorRegistryInvariantAuditReusesTheMaterializePredicate is the
// duplication guard. The section's counts are compared against the shipped
// planner's own refusalScope output over the same Registry, so a hand-written
// second opinion about which stored topology can be materialized would have to
// disagree with the planner to pass this test -- and it would fail the moment
// the planner changes a verdict.
func TestDoctorRegistryInvariantAuditReusesTheMaterializePredicate(t *testing.T) {
	registry, _ := auditFixture(t)
	target, err := tmuxSocketNameTarget(defaultAppSocket)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{}
	for i := range registry.Projects {
		plan, err := planRegistryTopology(context.Background(), nil, registry,
			selector.UIDPrefix+registry.Projects[i].Metadata.UID, nil, nil, target, nil)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		fatal, skipped := plan.refusalScope()
		for _, group := range []struct {
			scope string
			items []resourceReconcileItem
		}{{"fatal", fatal}, {"skipped", skipped}} {
			for _, item := range group.items {
				want[doctorRegistryRefusalCode(group.scope, strings.ToLower(item.Kind))]++
			}
		}
	}
	got := map[string]int{}
	for _, finding := range auditDoctor(t, registry).evaluateRegistryInvariants() {
		if strings.HasPrefix(finding.Code, doctorRegistryCodePrefix+"fatal.") || strings.HasPrefix(finding.Code, doctorRegistryCodePrefix+"skipped.") {
			got[finding.Code] = finding.Count
		}
	}
	if len(want) == 0 {
		t.Fatal("fixture produced no refusals, so the comparison proves nothing")
	}
	if !mapsEqualInt(want, got) {
		t.Fatalf("audit counts = %v, planner refusals = %v", got, want)
	}
}

func mapsEqualInt(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

// TestDoctorRegistryInvariantAuditWritesNothing is acceptance 3, run against the
// production zero-write read on a machine that has never created a Project.
func TestDoctorRegistryInvariantAuditWritesNothing(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	stateHome := filepath.Join(root, "state")
	configHome := filepath.Join(root, "config")
	for _, dir := range []string{home, stateHome, configHome} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("XDG_CONFIG_HOME", configHome)

	before := auditTreeSnapshot(t, root)
	cmd := newStubDoctorCommand("linux", map[string]bool{"tmux": true, "git": true, "stty": true})
	cmd.readRegistry = snapshotResourceRegistry

	findings := cmd.evaluateRegistryInvariants()
	if _, ok := auditFinding(findings, doctorRegistryCodeClean); !ok {
		t.Fatalf("first-use audit = %#v, want an explicit clean result", findings)
	}
	if _, err := os.Lstat(filepath.Join(stateHome, "projmux")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("audit created the state directory: err = %v", err)
	}
	if after := auditTreeSnapshot(t, root); !slices.Equal(before, after) {
		t.Fatalf("audit changed the filesystem\nbefore: %v\nafter:  %v", before, after)
	}
}

func auditTreeSnapshot(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, statErr := entry.Info()
		if statErr != nil {
			return statErr
		}
		size := int64(0)
		if !entry.IsDir() {
			size = info.Size()
		}
		out = append(out, fmt.Sprintf("%s|%v|%o|%d", path, entry.IsDir(), info.Mode().Perm(), size))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// TestDoctorRegistryInvariantAuditDegradesWhenTheRegistryCannotBeRead keeps an
// unreadable Registry a reported finding rather than a failed diagnostic.
func TestDoctorRegistryInvariantAuditDegradesWhenTheRegistryCannotBeRead(t *testing.T) {
	cmd := newStubDoctorCommand("linux", map[string]bool{"tmux": true, "git": true, "stty": true})
	cmd.readRegistry = func() (coremetadata.Registry, error) {
		return coremetadata.Registry{}, errors.New("registry unreadable")
	}

	findings := cmd.evaluateRegistryInvariants()
	if len(findings) != 1 || findings[0].Code != doctorRegistryCodeUnavailable {
		t.Fatalf("unreadable Registry findings = %#v", findings)
	}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"--section", "registry"}, &stdout, io.Discard); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(stdout.String(), doctorRegistryCodeUnavailable) {
		t.Fatalf("unavailable audit not reported:\n%s", stdout.String())
	}
}

// TestDoctorRegistryInvariantAuditNeverReachesATmuxServer pins the offline
// contract: the runner the audit hands the planner refuses every call, and the
// audit still produces a verdict.
func TestDoctorRegistryInvariantAuditNeverReachesATmuxServer(t *testing.T) {
	if _, err := (doctorOfflineTmuxRunner{}).Run(context.Background(), "tmux", "list-windows"); !errors.Is(err, errDoctorRegistryAuditOffline) {
		t.Fatalf("offline runner error = %v, want the offline refusal", err)
	}
	registry, _ := auditFixture(t)
	if findings := auditDoctor(t, registry).evaluateRegistryInvariants(); len(findings) < 2 {
		t.Fatalf("offline audit produced %#v", findings)
	}
}

// TestDoctorRegistryInvariantCodesStayInsideThePublishedInventory keeps every
// emitted code inside the inventory the support-report allowlist is built from.
// A code outside it would reach a support archive as an opaque hash.
func TestDoctorRegistryInvariantCodesStayInsideThePublishedInventory(t *testing.T) {
	registry, _ := auditFixture(t)
	safe := supportDoctorSafeStringValues()
	emitted := auditDoctor(t, registry).evaluateRegistryInvariants()
	emitted = append(emitted, doctorRegistryUnavailableFinding())
	emitted = append(emitted, auditDoctor(t, auditHealthyFixture(t)).evaluateRegistryInvariants()...)
	for _, finding := range emitted {
		if !slices.Contains(doctorRegistryAuditCodeInventory, finding.Code) {
			t.Fatalf("emitted code %q is not in the audit inventory", finding.Code)
		}
		if !safe["code"][finding.Code] {
			t.Fatalf("emitted code %q is not support-report safe", finding.Code)
		}
		if !safe["remediation"][finding.Remediation] {
			t.Fatalf("emitted remediation %q is not support-report safe", finding.Remediation)
		}
	}
	for _, code := range doctorRegistryAuditCodeInventory {
		if !slices.Contains(doctorFindingCodeInventory, code) {
			t.Fatalf("audit code %q is missing from the shared finding inventory", code)
		}
	}
}

// TestSupportReportCarriesRegistryInvariantCountsWithoutReasonsOrPaths is
// acceptance 4: the archive carries the same counts and kinds, and neither the
// planner's reason wording nor any stored absolute path.
func TestSupportReportCarriesRegistryInvariantCountsWithoutReasonsOrPaths(t *testing.T) {
	cmd, stateHome, configHome := testReportCommand(t)
	seedReportSources(t, stateHome, configHome)
	registry, goneRoot := auditFixture(t)
	cmd.doctor.readRegistry = func() (coremetadata.Registry, error) { return registry, nil }

	want := cmd.doctor.evaluateRegistryInvariants()
	wantDivergences := cmd.doctor.evaluateRegistryDivergences()
	output := filepath.Join(t.TempDir(), "report.tar.gz")
	if err := cmd.Run([]string{"report", "--output", output}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	entries, archiveText := readSupportArchive(t, output)
	var doctorReport struct {
		RegistryInvariants  []doctorFinding                 `json:"registry_invariants"`
		RegistryDivergences []resourcegraph.DivergenceCount `json:"registry_divergences"`
	}
	if err := json.Unmarshal(entries["doctor.json"], &doctorReport); err != nil {
		t.Fatal(err)
	}
	if len(doctorReport.RegistryInvariants) != len(want) {
		t.Fatalf("archive registry findings = %#v, want %#v", doctorReport.RegistryInvariants, want)
	}
	if !slices.Equal(doctorReport.RegistryDivergences, wantDivergences) || len(doctorReport.RegistryDivergences) != 6 {
		t.Fatalf("archive divergence counts = %#v, want count-only %#v", doctorReport.RegistryDivergences, wantDivergences)
	}
	for i, got := range doctorReport.RegistryInvariants {
		if got.Code != want[i].Code || got.Count != want[i].Count || got.Severity != want[i].Severity || got.Remediation != want[i].Remediation {
			t.Fatalf("archive row %d = %#v, want the doctor row %#v", i, got, want[i])
		}
		if len(got.Details) != 0 {
			t.Fatalf("archive row %d carried operator-only detail: %#v", i, got)
		}
	}
	for _, reason := range []string{"primaryPaneRef", "is not an existing directory", goneRoot} {
		if bytes.Contains(archiveText, []byte(reason)) {
			t.Fatalf("support archive leaked refusal detail %q:\n%s", reason, entries["doctor.json"])
		}
	}
	for _, safe := range []string{`"code": "registry.materialize.fatal.project"`, `"count": 1`} {
		if !bytes.Contains(entries["doctor.json"], []byte(safe)) {
			t.Fatalf("support archive lost safe audit value %q:\n%s", safe, entries["doctor.json"])
		}
	}
	if runtime.GOOS != "windows" && !bytes.Contains(entries["doctor.json"], []byte(`"remediation": "inspect-registry-topology"`)) {
		t.Fatalf("support archive lost the audit remediation:\n%s", entries["doctor.json"])
	}
}
