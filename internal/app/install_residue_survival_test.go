package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// survivalLedger writes records as ledger rows and returns a command that reads
// them.
func survivalLedger(t *testing.T, records []installResidueRecord) (*installResidueCommand, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, installResidueLedgerFile)
	var buf bytes.Buffer
	for _, record := range records {
		body, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("marshal record: %v", err)
		}
		buf.Write(body)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write ledger: %v", err)
	}
	return &installResidueCommand{stateDir: func() (string, error) { return dir, nil }}, path
}

// survivalRecord is one ledger row with recorded start instants.
func survivalRecord(at time.Time, role string, starts ...time.Time) installResidueRecord {
	row := projmuxProcessRoleVintage{Role: role, Processes: len(starts), Replaced: len(starts)}
	for _, start := range starts {
		row.ReplacedAgeSeconds = append(row.ReplacedAgeSeconds, int(at.Sub(start)/time.Second))
		row.ReplacedStartedAtUnix = append(row.ReplacedStartedAtUnix, start.Unix())
	}
	return installResidueRecord{
		At: at.Format(time.RFC3339), Installer: "make", Supported: true,
		Observed: len(starts), Replaced: len(starts),
		Roles: []projmuxProcessRoleVintage{row},
	}
}

func survivalCutoff(t *testing.T, report installResidueSurvivalReport, seconds int) installResidueSurvivalCutoff {
	t.Helper()
	for _, cutoff := range report.Cutoffs {
		if cutoff.Seconds == seconds {
			return cutoff
		}
	}
	t.Fatalf("report has no cutoff at %ds; it carries %+v", seconds, report.Cutoffs)
	return installResidueSurvivalCutoff{}
}

// TestInstallResidueSurvivalCountsProcessesNotObservations is the measurement
// defence this route exists for.
//
// The ledger records one census per install and installs land minutes apart, so
// a long-lived process is recorded again in record after record. Reading a
// cutoff off the samples weights every process by the number of installs it
// survived -- which is the quantity being measured -- and reports a drain as
// far harder than it is. The fixture below is that bias in miniature: one
// process that outlives every install and four that do not.
func TestInstallResidueSurvivalCountsProcessesNotObservations(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	old := base.Add(-100 * time.Hour)
	var records []installResidueRecord
	for i := range 10 {
		at := base.Add(time.Duration(i) * 5 * time.Minute)
		records = append(records, survivalRecord(at, projmuxProcessRoleSupervisor, old))
	}
	// Four short-lived supervisors, each seen exactly once.
	for i := range 4 {
		at := base.Add(time.Duration(i) * time.Hour)
		records = append(records, survivalRecord(at, projmuxProcessRoleSupervisor, at.Add(-time.Minute)))
	}

	report := installResidueSurvival(records)
	if report.Records != 14 || report.Observations != 14 {
		t.Fatalf("records/observations = %d/%d, want 14/14", report.Records, report.Observations)
	}
	if report.Processes != 5 {
		t.Fatalf("processes = %d, want the ten samples of one process counted once", report.Processes)
	}
	if report.DerivedProcesses != 0 {
		t.Fatalf("derived processes = %d, want none when every record carries a start instant", report.DerivedProcesses)
	}
	day := survivalCutoff(t, report, 24*3600)
	if day.Observations != 10 {
		t.Fatalf("24h observations = %d, want the ten samples of the long-lived process", day.Observations)
	}
	if day.Processes != 1 {
		t.Fatalf("24h processes = %d, want one", day.Processes)
	}
	// The two bases disagree by seven times here, which is the whole point:
	// 71% of observations against 20% of processes.
	if day.Observations*report.Processes <= day.Processes*report.Observations {
		t.Fatalf("observation basis %d/%d did not exceed identity basis %d/%d",
			day.Observations, report.Observations, day.Processes, report.Processes)
	}
}

// TestInstallResidueSurvivalKeepsEachProcessAtItsOldestObservedAge pins that a
// process is counted at the greatest age it was ever seen at.
//
// A process is recorded at a different age in every record it survives. Taking
// the first would understate every lifetime by the whole span of the ledger,
// and taking a mean would answer a question nobody asked. The greatest observed
// age is the only one that is a real lower bound on the lifetime.
func TestInstallResidueSurvivalKeepsEachProcessAtItsOldestObservedAge(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	start := base.Add(-30 * time.Minute)
	report := installResidueSurvival([]installResidueRecord{
		survivalRecord(base, projmuxProcessRoleSupervisor, start),
		survivalRecord(base.Add(40*time.Minute), projmuxProcessRoleSupervisor, start),
	})
	if report.Processes != 1 {
		t.Fatalf("processes = %d, want one", report.Processes)
	}
	hour := survivalCutoff(t, report, 3600)
	if hour.Processes != 1 {
		t.Fatalf("1h processes = %d, want the process counted at the 70 minutes it reached, not the 30 it was first seen at", hour.Processes)
	}
	if hour.Observations != 1 {
		t.Fatalf("1h observations = %d, want only the second sample past the cutoff", hour.Observations)
	}
}

// TestInstallResidueSurvivalRecordedStartInstantsCompareExactly holds the line
// between the two identity sources.
//
// A recorded start instant is exact, so two processes that started one second
// apart are two processes. Applying the reconstruction's slack to recorded
// instants would merge them and quietly undercount a fleet whose panes are
// created in bursts, which is exactly how panes are created.
func TestInstallResidueSurvivalRecordedStartInstantsCompareExactly(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	report := installResidueSurvival([]installResidueRecord{
		survivalRecord(base, projmuxProcessRoleSupervisor,
			base.Add(-2*time.Hour), base.Add(-2*time.Hour+time.Second), base.Add(-2*time.Hour+2*time.Second)),
	})
	if report.Processes != 3 {
		t.Fatalf("processes = %d, want three exact start instants to stay three processes", report.Processes)
	}
	if report.DerivedProcesses != 0 {
		t.Fatalf("derived = %d, want none", report.DerivedProcesses)
	}
}

// TestInstallResidueSurvivalReconstructedStartsAbsorbTheirOwnSecondOfSlack
// covers the records written before the start instant was recorded.
//
// Both the record instant and the age are truncated to whole seconds, so
// subtracting one from the other lands on the true start second or the one
// after it depending on where the two truncations fell. The same process
// therefore reconstructs to two different keys across two records. One second
// of slack is the exact size of that ambiguity, and the report has to say how
// much of its count rests on it.
func TestInstallResidueSurvivalReconstructedStartsAbsorbTheirOwnSecondOfSlack(t *testing.T) {
	t.Parallel()

	legacy := func(at time.Time, ages ...int) installResidueRecord {
		return installResidueRecord{
			At: at.Format(time.RFC3339), Installer: "make", Supported: true,
			Observed: len(ages), Replaced: len(ages),
			Roles: []projmuxProcessRoleVintage{
				{Role: projmuxProcessRoleSupervisor, Processes: len(ages), Replaced: len(ages), ReplacedAgeSeconds: ages},
			},
		}
	}
	base := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	// One process, seen twice. The second reading truncated the other way, so
	// its reconstructed start is one second later than the first.
	report := installResidueSurvival([]installResidueRecord{
		legacy(base, 7200),
		legacy(base.Add(time.Hour), 10799),
	})
	if report.Processes != 1 {
		t.Fatalf("processes = %d, want the one-second reconstruction slack absorbed", report.Processes)
	}
	if report.DerivedProcesses != 1 {
		t.Fatalf("derived processes = %d, want the report to say the identity is reconstructed", report.DerivedProcesses)
	}
	if text := renderInstallResidueSurvival(report); !strings.Contains(text, "reconstructed") {
		t.Fatalf("rendered report = %q, want it to disclose the reconstruction", text)
	}

	// Two seconds apart is beyond the ambiguity, so it stays two processes.
	two := installResidueSurvival([]installResidueRecord{legacy(base, 7200), legacy(base.Add(time.Hour), 10798)})
	if two.Processes != 2 {
		t.Fatalf("processes = %d, want two starts beyond the one-second ambiguity to stay two", two.Processes)
	}
}

// TestInstallResidueSurvivalDoesNotChainAcrossNearSimultaneousStarts bounds the
// slack.
//
// Grouping compares a sample against each group's first member rather than its
// latest. Comparing against the latest would let a run of starts one second
// apart chain into a single group and collapse a whole burst of panes into one
// process -- an unbounded error from a bounded ambiguity.
func TestInstallResidueSurvivalDoesNotChainAcrossNearSimultaneousStarts(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	record := installResidueRecord{
		At: base.Format(time.RFC3339), Installer: "make", Supported: true,
		Observed: 5, Replaced: 5,
		Roles: []projmuxProcessRoleVintage{{
			Role: projmuxProcessRoleSupervisor, Processes: 5, Replaced: 5,
			// Five starts, each one second after the last.
			ReplacedAgeSeconds: []int{7196, 7197, 7198, 7199, 7200},
		}},
	}
	report := installResidueSurvival([]installResidueRecord{record})
	if report.Processes != 3 {
		t.Fatalf("processes = %d, want the slack to pair neighbours without chaining the whole run into one", report.Processes)
	}
}

// TestInstallResidueSurvivalIgnoresRecordsWhoseCensusObservedNothing keeps a
// silent record from diluting the rate.
//
// A record whose own census observed nothing says nothing about the fleet --
// it is a census taken from a build that is not the installed one, or on a
// platform with no process table. Counting it as a record with no residue
// would report an install as clean on evidence that never looked.
func TestInstallResidueSurvivalIgnoresRecordsWhoseCensusObservedNothing(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	report := installResidueSurvival([]installResidueRecord{
		{At: base.Format(time.RFC3339), Supported: true},
		{At: base.Add(time.Minute).Format(time.RFC3339), Supported: false},
		survivalRecord(base.Add(2*time.Minute), projmuxProcessRoleSupervisor, base.Add(-time.Hour)),
	})
	if report.Records != 1 {
		t.Fatalf("records = %d, want only the record whose census observed something", report.Records)
	}
	if report.Processes != 1 || report.Observations != 1 {
		t.Fatalf("processes/observations = %d/%d, want 1/1", report.Processes, report.Observations)
	}
}

// TestInstallResidueSurvivalCarriesNoProcessIdentity is the negative audit for
// the second surface this Phase adds.
//
// The census promises that no pid, path, or argv reaches a diagnostics surface.
// This report reads the ledger, which by construction contains none of those,
// and it must not reintroduce one by rendering a start instant as a wall clock
// an operator could correlate against, or by naming anything but roles and
// counters.
func TestInstallResidueSurvivalCarriesNoProcessIdentity(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	start := base.Add(-100 * time.Hour)
	text := renderInstallResidueSurvival(installResidueSurvival([]installResidueRecord{
		survivalRecord(base, projmuxProcessRoleSupervisor, start),
		survivalRecord(base.Add(5*time.Minute), projmuxProcessRoleSessionClient, start),
	}))
	for _, leak := range []string{
		"pane-", "agent-", "/", "\\", "projmux ", "internal",
		strconv.FormatInt(start.Unix(), 10), base.Format(time.RFC3339),
	} {
		if strings.Contains(text, leak) {
			t.Fatalf("survival report = %q, want no process identity; found %q", text, leak)
		}
	}
}

// TestInstallResidueSurvivalRouteAnswersOrSaysWhyItCannot separates the two
// halves of this route's error contract.
//
// The census path runs as the last step of a successful install and must never
// fail it. The survival path is a reader's question with no install behind it,
// so an unreadable ledger is an answer it owes rather than a failure it has to
// swallow.
func TestInstallResidueSurvivalRouteAnswersOrSaysWhyItCannot(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	command, _ := survivalLedger(t, []installResidueRecord{
		survivalRecord(base, projmuxProcessRoleSupervisor, base.Add(-90*time.Minute)),
	})
	var out bytes.Buffer
	if err := command.Survival(&out); err != nil {
		t.Fatalf("Survival() error = %v, want nil", err)
	}
	for _, want := range []string{"by observation", "by process identity", "1h00m"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("survival report = %q, want it to contain %q", out.String(), want)
		}
	}

	missing := &installResidueCommand{stateDir: func() (string, error) { return t.TempDir(), nil }}
	if err := missing.Survival(&out); err == nil {
		t.Fatal("Survival() on an absent ledger returned nil, want the reason a reader can act on")
	}

	if !installResidueRouteWantsSurvival([]string{installResidueSurvivalFlag}) {
		t.Fatal("the survival flag is not recognised on the route")
	}
	if installResidueRouteWantsSurvival(nil) {
		t.Fatal("an install invocation was read as a survival query")
	}
}

// TestReplacementMeasurementPathEndsNoProcess is the scope guard for this
// layer.
//
// `L2` is the layer where an install *fails* to replace a running process, so
// it is the layer where a measurement is one edit away from becoming a
// remedy. This Phase names roles and counts survivals and does neither. The
// files below are the whole measurement path, and none of them may acquire the
// ability to end, start, signal, or restart anything -- that behavior belongs
// to a separate design with its own cutoff decision, and a drain that arrived
// as a side effect of a census would be the worst possible way to get one.
func TestReplacementMeasurementPathEndsNoProcess(t *testing.T) {
	t.Parallel()

	for _, file := range []string{
		"internal/app/codex_controlplane_vintage.go",
		"internal/app/install_residue.go",
		"internal/app/install_residue_survival.go",
		"internal/app/doctor_replacement.go",
	} {
		body := readRepoText(t, file)
		for _, forbidden := range []string{
			"os/exec", "exec.Command", "syscall.Kill", "Process.Kill", "Process.Signal",
			"SIGTERM", "SIGKILL", "cmd.Start(", "cmd.Run(",
		} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("%s mentions %q; this path measures replacement and must not perform one", file, forbidden)
			}
		}
	}
}
