package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
)

// Reading the install residue ledger as a survival question.
//
// The ledger answers "how much did this install leave behind" once per install.
// A replacement design asks a different question of the same file -- what
// fraction of residual processes outlive a cutoff T -- and that question is
// about processes, while the ledger is a sequence of observations.
//
// The two are not the same count, and on this repository's own ledger they are
// not close. Installs land minutes apart, so one long-lived process is recorded
// again in install after install and contributes as many samples as there were
// installs it survived. Counting samples weights the long-lived processes by
// their own longevity, which is precisely the quantity being measured, and
// inflates every cutoff.
//
// This file deduplicates by process identity and reports both numbers, because
// the difference between them is itself the finding.
//
// Nothing here reads the process table or touches a process. It is one bounded
// read of a JSON Lines file this application wrote.

// installResidueSurvivalFlag is the argv word that selects this report instead
// of taking a census.
const installResidueSurvivalFlag = "--survival"

// installResidueSurvivalCutoffs are the T values the report answers for.
//
// They span the range a bounded drain would be designed in: an hour is the
// shortest cutoff worth naming, and a week is where this ledger's own
// distribution runs out.
var installResidueSurvivalCutoffs = []time.Duration{
	time.Hour,
	4 * time.Hour,
	12 * time.Hour,
	24 * time.Hour,
	72 * time.Hour,
	168 * time.Hour,
}

// installResidueStartSlack is how far apart two reconstructed start instants
// may be and still be one process.
//
// A record carries whole-second instants and whole-second ages, and both are
// truncated, so a start reconstructed by subtraction lands on the true second
// or the one after it. One second is therefore the exact ambiguity of the
// reconstruction -- not a tuning value -- and it applies only where at least
// one of the two observations was reconstructed. Recorded start instants
// compare exactly.
const installResidueStartSlack = int64(1)

// installResidueProcess is one residual process as the ledger can identify it.
//
// Identity is the pair (role, start instant). It names no process: there is no
// pid, no path, and no argv in the ledger to name one with, which is the whole
// reason this is the strongest key available.
type installResidueProcess struct {
	// Role is the census role this process was counted under.
	Role string
	// StartedAtUnix is the start instant identity is keyed on.
	StartedAtUnix int64
	// Derived reports that this identity rests on a start reconstructed from
	// an age rather than on a recorded one, and is therefore accurate to a
	// second rather than exactly.
	Derived bool
	// OldestAgeSeconds is the greatest age this process was ever observed at.
	// It is a lower bound on its lifetime: the process may still be running.
	OldestAgeSeconds int
	// Observations is how many ledger samples resolved to this process.
	Observations int
}

// installResidueSurvivalCutoff is one cutoff's answer on both bases.
type installResidueSurvivalCutoff struct {
	Seconds int
	// Observations is how many residual samples were taken at or beyond T.
	Observations int
	// Processes is how many distinct processes were ever observed at or
	// beyond T.
	Processes int
}

// installResidueSurvivalReport is the whole answer.
type installResidueSurvivalReport struct {
	// Records is how many ledger records carried a census that observed
	// anything. A record whose own census observed nothing says nothing about
	// the fleet and is not a sample of it.
	Records int
	// Observations is how many residual age samples those records carried.
	Observations int
	// Processes is how many distinct processes they deduplicate to.
	Processes int
	// DerivedProcesses is how many of those identities rest on a
	// reconstructed start instant.
	DerivedProcesses int
	// Cutoffs is the survival answer at each T.
	Cutoffs []installResidueSurvivalCutoff
}

// installResidueSurvival deduplicates the ledger's residual samples by process
// and answers each cutoff on both bases.
//
// Records are read in the order they were written. Within one role the samples
// are grouped by start instant; a reconstructed instant may join a group whose
// first member is within the reconstruction's own one second of slack, and a
// recorded instant joins only an exact match. Grouping compares against each
// group's first member rather than its latest, so the slack cannot chain across
// a run of near-simultaneous starts and collapse them all into one.
func installResidueSurvival(records []installResidueRecord) installResidueSurvivalReport {
	cutoffs := make([]installResidueSurvivalCutoff, 0, len(installResidueSurvivalCutoffs))
	for _, cutoff := range installResidueSurvivalCutoffs {
		cutoffs = append(cutoffs, installResidueSurvivalCutoff{Seconds: int(cutoff / time.Second)})
	}

	groups := map[string][]*installResidueProcess{}
	report := installResidueSurvivalReport{}
	for _, record := range records {
		if record.Observed <= 0 {
			continue
		}
		report.Records++
		at, err := time.Parse(time.RFC3339, strings.TrimSpace(record.At))
		if err != nil {
			continue
		}
		for _, role := range record.Roles {
			for _, sample := range installResidueRoleSamples(role, at) {
				report.Observations++
				for index := range cutoffs {
					if sample.AgeSeconds >= cutoffs[index].Seconds {
						cutoffs[index].Observations++
					}
				}
				installResidueAdmitSample(groups, role.Role, sample)
			}
		}
	}

	for _, role := range groups {
		for _, process := range role {
			report.Processes++
			if process.Derived {
				report.DerivedProcesses++
			}
			for index := range cutoffs {
				if process.OldestAgeSeconds >= cutoffs[index].Seconds {
					cutoffs[index].Processes++
				}
			}
		}
	}
	report.Cutoffs = cutoffs
	return report
}

// installResidueSample is one residual process as one record observed it.
type installResidueSample struct {
	StartedAtUnix int64
	AgeSeconds    int
	Derived       bool
}

// installResidueRoleSamples reads one role's residual samples out of one
// record.
//
// A record written with recorded start instants is read from those, and the age
// is recomputed from the record's own instant so that both facts come from the
// same pair of numbers. A record written before that field existed is read from
// its age distribution, with the start reconstructed and marked as such.
func installResidueRoleSamples(role projmuxProcessRoleVintage, at time.Time) []installResidueSample {
	if len(role.ReplacedStartedAtUnix) > 0 {
		samples := make([]installResidueSample, 0, len(role.ReplacedStartedAtUnix))
		for _, start := range role.ReplacedStartedAtUnix {
			age := max(int(at.Unix()-start), 0)
			samples = append(samples, installResidueSample{StartedAtUnix: start, AgeSeconds: age})
		}
		return samples
	}
	samples := make([]installResidueSample, 0, len(role.ReplacedAgeSeconds))
	for _, age := range role.ReplacedAgeSeconds {
		samples = append(samples, installResidueSample{
			StartedAtUnix: at.Unix() - int64(age),
			AgeSeconds:    age,
			Derived:       true,
		})
	}
	return samples
}

// installResidueAdmitSample folds one sample into its role's process groups.
func installResidueAdmitSample(groups map[string][]*installResidueProcess, role string, sample installResidueSample) {
	for _, process := range groups[role] {
		if !installResidueSameProcess(process, sample) {
			continue
		}
		process.Observations++
		process.Derived = process.Derived || sample.Derived
		process.OldestAgeSeconds = max(process.OldestAgeSeconds, sample.AgeSeconds)
		return
	}
	groups[role] = append(groups[role], &installResidueProcess{
		Role:             role,
		StartedAtUnix:    sample.StartedAtUnix,
		Derived:          sample.Derived,
		OldestAgeSeconds: sample.AgeSeconds,
		Observations:     1,
	})
}

// installResidueSameProcess decides whether one sample belongs to a group.
func installResidueSameProcess(process *installResidueProcess, sample installResidueSample) bool {
	distance := process.StartedAtUnix - sample.StartedAtUnix
	if distance < 0 {
		distance = -distance
	}
	if distance == 0 {
		return true
	}
	// Only a reconstruction is uncertain, and only by its own one second.
	if !process.Derived && !sample.Derived {
		return false
	}
	return distance <= installResidueStartSlack
}

// renderInstallResidueSurvival writes the report the way both numbers have to
// be read: side by side, because either one alone is a claim about the other.
func renderInstallResidueSurvival(report installResidueSurvivalReport) string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Install residue survival across %d ledger %s\n\n",
		report.Records, pluralizeInstallResidueRecords(report.Records))
	if report.Observations == 0 {
		buf.WriteString("  No residual process was ever recorded with a readable start time.\n")
		return buf.String()
	}
	fmt.Fprintf(&buf, "  %-8s  %-23s  %s\n", "cutoff", "by observation", "by process identity")
	for _, cutoff := range report.Cutoffs {
		fmt.Fprintf(&buf, "  %-8s  %s  %s\n",
			formatInstallResidueAge(int64(cutoff.Seconds)),
			installResidueSurvivalCell(cutoff.Observations, report.Observations),
			installResidueSurvivalCell(cutoff.Processes, report.Processes))
	}
	fmt.Fprintf(&buf, "\n  %d residual observations deduplicate to %d processes.\n",
		report.Observations, report.Processes)
	if report.DerivedProcesses > 0 {
		fmt.Fprintf(&buf,
			"  %d of them rest on a start instant reconstructed from an age, which is\n"+
				"  exact to one second; those records predate the recorded start instant.\n",
			report.DerivedProcesses)
	}
	return buf.String()
}

func pluralizeInstallResidueRecords(count int) string {
	if count == 1 {
		return "record"
	}
	return "records"
}

// installResidueSurvivalCell renders one basis's count and share.
func installResidueSurvivalCell(part, whole int) string {
	share := 0.0
	if whole > 0 {
		share = 100 * float64(part) / float64(whole)
	}
	return fmt.Sprintf("%6d of %-6d %5.1f%%", part, whole, share)
}

// parseInstallResidueLedgerRecords parses the ledger.
//
// A row that does not parse is skipped rather than failing the read: the ledger
// is append-only and a partial final write must not cost the whole history.
func parseInstallResidueLedgerRecords(path string) ([]installResidueRecord, bool) {
	lines, ok := readInstallResidueLedgerLines(path)
	if !ok {
		return nil, false
	}
	records := make([]installResidueRecord, 0, len(lines))
	for _, line := range lines {
		var record installResidueRecord
		if err := json.Unmarshal(line, &record); err != nil {
			continue
		}
		records = append(records, record)
	}
	return records, true
}

// runInstallResidueSurvival is the `--survival` route.
//
// Unlike the census route this one is a question a reader asked, so an
// unreadable ledger is an error rather than a swallowed failure: there is no
// install for it to endanger, and answering a survival question with silence
// would be worse than answering it with the reason.
func (c *installResidueCommand) Survival(stdout io.Writer) error {
	path := c.ledgerPath()
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("install residue ledger location is unavailable")
	}
	records, ok := parseInstallResidueLedgerRecords(path)
	if !ok {
		return fmt.Errorf("install residue ledger is unreadable: %s", path)
	}
	if stdout == nil {
		return nil
	}
	_, err := io.WriteString(stdout, renderInstallResidueSurvival(installResidueSurvival(records)))
	return err
}

// installResidueRouteWantsSurvival reports whether this invocation asked for
// the survival report.
func installResidueRouteWantsSurvival(args []string) bool {
	return slices.Contains(args, installResidueSurvivalFlag)
}
