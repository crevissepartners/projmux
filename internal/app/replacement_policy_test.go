package app

import (
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// replacementPolicyDocRow matches one row of the published disposition table.
var replacementPolicyDocRow = regexp.MustCompile("^\\|\\s*`([a-z-]+)`\\s*\\|\\s*`([a-z-]+)`\\s*\\|\\s*`([a-z-]+)`\\s*\\|$")

// TestReplacementRolePoliciesMatchTheContractDocument is the policy half of the
// vocabulary drift guard.
//
// The disposition table decides which running processes an install is allowed
// to end. A table that drifts from the document is worse than an undocumented
// one: a reader deciding whether to run an install consults the document, and a
// code path that ends a role the document calls report-only would be exactly the
// surprise this whole track exists to remove. The comparison runs in both
// directions, so neither side can gain or lose a role on its own.
func TestReplacementRolePoliciesMatchTheContractDocument(t *testing.T) {
	t.Parallel()

	published := map[string]replacementRolePolicy{}
	for line := range strings.SplitSeq(replacementPolicySection(t), "\n") {
		match := replacementPolicyDocRow.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}
		published[match[1]] = replacementRolePolicy{Disposition: match[2], Route: match[3]}
	}
	if len(published) == 0 {
		t.Fatal("docs/replacement-contract.md publishes no role dispositions")
	}
	if !maps.Equal(published, replacementRolePolicies) {
		t.Fatalf("role dispositions: docs/replacement-contract.md has %v, code has %v",
			published, replacementRolePolicies)
	}

	// Every role the census can name has a decision. A role with no entry
	// falls back to report-only, which is the safe answer, but relying on that
	// fallback for a named role would leave the decision unstated.
	for _, role := range projmuxProcessRoleOrder {
		if _, ok := replacementRolePolicies[role]; !ok {
			t.Fatalf("census role %q has no published disposition", role)
		}
	}
	if len(replacementRolePolicies) != len(projmuxProcessRoleOrder) {
		t.Fatalf("policy table has %d roles, census order has %d",
			len(replacementRolePolicies), len(projmuxProcessRoleOrder))
	}

	// Only the two dispositions this contract defines exist. A third value
	// would be a way to sever work without the document saying so.
	for role, policy := range replacementRolePolicies {
		if policy.Disposition != replacementDispositionDrain &&
			policy.Disposition != replacementDispositionReportOnly {
			t.Fatalf("role %q has disposition %q, want drain or report-only", role, policy.Disposition)
		}
	}
}

// TestReplacementSessionClientIsNeverADrainTarget holds the one entry in the
// table that is a decision rather than an observation.
//
// `session-client` is the operator's own attached tmux session -- the longest
// role on this repository's ledger at 169h29m -- so a uniform replacement policy
// ends the terminal the install was typed into. The architecture decision this
// phase implements fixes it as report-only, and this test is what makes that a
// property of the source rather than of whoever last read the document.
func TestReplacementSessionClientIsNeverADrainTarget(t *testing.T) {
	t.Parallel()

	if got := replacementRoleDisposition(projmuxProcessRoleSessionClient); got != replacementDispositionReportOnly {
		t.Fatalf("session-client disposition = %q, want %q", got, replacementDispositionReportOnly)
	}

	// A fleet that is nothing but attached sessions produces no drain target,
	// however old and however far past the cutoff those sessions are.
	fleet := []projmuxProcessRoleVintage{
		{Role: projmuxProcessRoleSessionClient, Processes: 3, Replaced: 3, ReplacedAgeSeconds: []int{610000, 620000, 630000}},
	}
	drain, report := replacementResidualByDisposition(fleet)
	if drain != 0 || report != 3 {
		t.Fatalf("attached-session fleet = %d drain / %d report, want 0 / 3", drain, report)
	}

	// And an install pass over that fleet asks for nothing.
	outcome := runTestInstallReplacement(t, installReplacementFixture{
		vintage: projmuxProcessVintage{Supported: true, Roles: fleet},
	})
	if outcome.Outcome != installReplacementOutcomeNoTarget || outcome.Attempted != 0 {
		t.Fatalf("pass over an attached-session fleet = %+v, want no target", outcome)
	}
}

// TestReplacementDrainCutoffIsTheAdoptedValueAndBound fixes the cutoff, its
// override, and the boundary the verdict turns on.
//
// The value is a measurement result: 24h truncates 9.5% of residual processes
// by identity on this repository's ledger, 13 of 137. Changing the constant
// without redoing that measurement changes what fraction of ordinary drains get
// reported as failures, so the number is held here rather than left to a
// comment.
func TestReplacementDrainCutoffIsTheAdoptedValueAndBound(t *testing.T) {
	t.Parallel()

	if replacementDrainCutoff != 24*time.Hour {
		t.Fatalf("adopted cutoff = %s, want 24h (truncation rate 9.5%%, 13 of 137 by process identity)",
			replacementDrainCutoff)
	}

	for _, tc := range []struct {
		name string
		raw  string
		want time.Duration
	}{
		{name: "unset", raw: "", want: replacementDrainCutoff},
		{name: "override", raw: "90m", want: 90 * time.Minute},
		{name: "unparsable", raw: "soon", want: replacementDrainCutoff},
		// There is no spelling that disables the bound. An unbounded drain is
		// the state the cutoff rules out, and zero or negative must not be a
		// back door to it.
		{name: "zero", raw: "0s", want: replacementDrainCutoff},
		{name: "negative", raw: "-1h", want: replacementDrainCutoff},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveReplacementCutoff(func(key string) string {
				if key == replacementCutoffEnv {
					return tc.raw
				}
				return ""
			})
			if got != tc.want {
				t.Fatalf("cutoff for %q = %s, want %s", tc.raw, got, tc.want)
			}
		})
	}

	// The boundary is exclusive: a process exactly at the cutoff has not
	// outlived it. Ages are whole seconds, so an inclusive bound would report a
	// replacement as failed one truncation early.
	bound := int(replacementDrainCutoff / time.Second)
	roles := []projmuxProcessRoleVintage{{
		Role: projmuxProcessRoleSupervisor, Processes: 3, Replaced: 3,
		ReplacedAgeSeconds: []int{bound - 1, bound, bound + 1},
	}}
	if got := replacementResidualBeyondCutoff(roles, replacementDrainCutoff); got != 1 {
		t.Fatalf("beyond cutoff = %d, want 1 (only the age past %d seconds)", got, bound)
	}

	// A residual process whose start the platform could not establish carries
	// no age sample and is not counted as past the cutoff. An unreadable age is
	// not evidence of an old process.
	untimed := []projmuxProcessRoleVintage{{Role: projmuxProcessRoleSupervisor, Processes: 2, Replaced: 2}}
	if got := replacementResidualBeyondCutoff(untimed, time.Second); got != 0 {
		t.Fatalf("beyond cutoff with no age samples = %d, want 0", got)
	}
}

// TestReplacementResidualSplitFollowsTheTable holds the split the install pass
// acts on: a residual count per disposition, with an unnamed role treated as
// report-only.
func TestReplacementResidualSplitFollowsTheTable(t *testing.T) {
	t.Parallel()

	drain, report := replacementResidualByDisposition([]projmuxProcessRoleVintage{
		{Role: codexControlPlaneRoleBroker, Processes: 1, Replaced: 1},
		{Role: projmuxProcessRoleSupervisor, Processes: 4, Current: 1, Replaced: 3},
		{Role: projmuxProcessRoleSessionClient, Processes: 1, Replaced: 1},
		// A role no census names today. It is report-only by fallback, which
		// is the answer that ends nothing on evidence nobody has read.
		{Role: "future-role", Processes: 2, Replaced: 2},
		// A role with no residue contributes to neither side.
		{Role: projmuxProcessRoleUsageWatcher, Processes: 1, Current: 1},
	})
	if drain != 1 {
		t.Fatalf("drain targets = %d, want 1", drain)
	}
	if report != 6 {
		t.Fatalf("report-only residue = %d, want 6", report)
	}
	if got := replacementRoleDisposition("future-role"); got != replacementDispositionReportOnly {
		t.Fatalf("unnamed role disposition = %q, want %q", got, replacementDispositionReportOnly)
	}
}

// replacementPolicySection returns the published disposition table's section.
func replacementPolicySection(t *testing.T) string {
	t.Helper()
	body := readRepoText(t, "docs/replacement-contract.md")
	_, after, ok := strings.Cut(body, "### Per-role disposition")
	if !ok {
		t.Fatal("docs/replacement-contract.md has no `### Per-role disposition` section")
	}
	if before, _, cut := strings.Cut(after, "\n### "); cut {
		after = before
	}
	return after
}

// TestReplacementPathEndsNoProcess is the scope guard for the acting half of
// this surface.
//
// The measurement path has one already. This one covers the policy table and
// the install pass, and it is the stronger claim of the two: these files decide
// what an install does to running processes, and the whole of what they are
// allowed to do is ask over a socket. A termination, a signal, or a spawn
// appearing here is the default this contract rejected arriving by the back
// door.
func TestReplacementPathEndsNoProcess(t *testing.T) {
	t.Parallel()

	for _, file := range []string{
		"internal/app/replacement_policy.go",
		"internal/app/install_replacement.go",
	} {
		body := readRepoText(t, file)
		for _, forbidden := range []string{
			"os/exec", "exec.Command", "syscall.Kill", "Process.Kill", "Process.Signal",
			"SIGTERM", "SIGKILL", "cmd.Start(", "cmd.Run(",
		} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("%s mentions %q; an install replacement asks and never ends", file, forbidden)
			}
		}
	}

	// The pass reaches a runtime by dialing it and never by ensuring one. An
	// Ensure would start what the pass was sent to replace.
	body := readRepoText(t, "internal/app/install_replacement.go")
	if strings.Contains(body, "codexbroker.Ensure") {
		t.Fatal("install_replacement.go calls Ensure; a replacement pass must never start a runtime")
	}
	if !strings.Contains(body, "codexbroker.Dial") {
		t.Fatal("install_replacement.go no longer reaches the runtime by dialing it")
	}
}

// TestReplacementDispositionTokensAreClosed keeps the two disposition spellings
// from drifting apart between the table and the document.
func TestReplacementDispositionTokensAreClosed(t *testing.T) {
	t.Parallel()

	want := []string{replacementDispositionDrain, replacementDispositionReportOnly}
	var got []string
	for _, policy := range replacementRolePolicies {
		if !slices.Contains(got, policy.Disposition) {
			got = append(got, policy.Disposition)
		}
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("dispositions in use = %v, want exactly %v", got, want)
	}
}
