package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// installResidueFixture builds a command whose census, clock, installer, and
// ledger location are all supplied, so the whole route runs without a process
// table and without the real state directory.
func installResidueFixture(t *testing.T, now time.Time, installer string, vintage projmuxProcessVintage) (*installResidueCommand, string) {
	t.Helper()
	dir := t.TempDir()
	return &installResidueCommand{
		now: func() time.Time { return now },
		getenv: func(name string) string {
			if name == "PROJMUX_INSTALLER" {
				return installer
			}
			return ""
		},
		stateDir:    func() (string, error) { return dir, nil },
		readVintage: func(time.Time) projmuxProcessVintage { return vintage },
	}, filepath.Join(dir, installResidueLedgerFile)
}

func readInstallResidueRecords(t *testing.T, path string) []installResidueRecord {
	t.Helper()
	payload, err := os.ReadFile(path) // #nosec G304 -- test-owned temp path.
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	var records []installResidueRecord
	for line := range strings.SplitSeq(strings.TrimSpace(string(payload)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var record installResidueRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("ledger row %q is not one JSON object: %v", line, err)
		}
		records = append(records, record)
	}
	return records
}

// residueFleet is a fleet census with the shape an install actually leaves
// behind: many supervisors, a few observers, one broker, all of them on the
// image the install replaced.
func residueFleet() projmuxProcessVintage {
	return projmuxProcessVintage{Supported: true, Roles: []projmuxProcessRoleVintage{
		{Role: codexControlPlaneRoleBroker, Processes: 1, Replaced: 1, ReplacedAgeSeconds: []int{8040}},
		{Role: codexControlPlaneRoleObserver, Processes: 4, Current: 1, Replaced: 3, ReplacedAgeSeconds: []int{2460, 2470, 8040}},
		{Role: projmuxProcessRoleSupervisor, Processes: 22, Current: 2, Replaced: 20, ReplacedAgeSeconds: []int{
			60, 120, 180, 240, 300, 600, 900, 1200, 1800, 2400,
			3000, 3300, 3600, 4200, 4800, 5400, 6000, 7200, 9000, 10920,
		}},
	}}
}

// TestInstallResidueReportsPerRoleResidualCountsAndAges is the measurement
// gate.
//
// The deliverable of this route is not the sentence, it is the three numbers a
// later automatic-replacement design has to choose a drain termination
// condition from: how many residual processes there are per role, how old they
// are as a distribution, and how often an install leaves any behind. A notice
// that printed only a warning would satisfy the wording and answer none of
// them.
func TestInstallResidueReportsPerRoleResidualCountsAndAges(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 6, 4, 12, 33, 0, time.UTC)
	cmd, ledger := installResidueFixture(t, now, "make", residueFleet())
	var stderr bytes.Buffer
	cmd.Run(&stderr)

	records := readInstallResidueRecords(t, ledger)
	if len(records) != 1 {
		t.Fatalf("ledger records = %d, want 1", len(records))
	}
	record := records[0]
	if record.At != "2026-09-06T04:12:33Z" {
		t.Fatalf("at = %q, want the census instant in RFC3339 UTC", record.At)
	}
	if record.Installer != "make" {
		t.Fatalf("installer = %q, want make", record.Installer)
	}
	if !record.Supported {
		t.Fatalf("supported = false, want the census to report itself as taken")
	}
	if record.Observed != 27 || record.Replaced != 24 {
		t.Fatalf("observed = %d, replaced = %d, want 27 and 24", record.Observed, record.Replaced)
	}
	if record.SinceLastInstallSeconds != nil {
		t.Fatalf("sinceLastInstallSeconds = %d, want it absent on the first record", *record.SinceLastInstallSeconds)
	}
	if len(record.Roles) != 3 {
		t.Fatalf("roles = %d, want the three roles with residual processes", len(record.Roles))
	}
	for _, role := range record.Roles {
		if len(role.ReplacedAgeSeconds) != role.Replaced {
			t.Fatalf("role %q carried %d ages for %d replaced processes, want the whole distribution",
				role.Role, len(role.ReplacedAgeSeconds), role.Replaced)
		}
		for index := 1; index < len(role.ReplacedAgeSeconds); index++ {
			if role.ReplacedAgeSeconds[index-1] > role.ReplacedAgeSeconds[index] {
				t.Fatalf("role %q ages = %v, want them ascending", role.Role, role.ReplacedAgeSeconds)
			}
		}
	}

	text := stderr.String()
	for _, want := range []string{
		"24 projmux processes are still running the image this install replaced",
		"supervisor",
		"lifecycle-observer",
		"broker-runtime",
		"oldest 3h02m",
		"oldest 2h14m",
		"median 45m",
		installResidueLedgerFile,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("notice = %q, want it to contain %q", text, want)
		}
	}
}

// TestInstallResidueZeroResidualPrintsNothing is the boundary that keeps this
// notice worth reading.
//
// An install that reached the whole fleet has nothing to report, and a line
// printed at every install is a line an operator learns to skip. The ledger
// still gets its record: "this install left nothing behind" is one of the
// frequency observations the drain design is derived from.
func TestInstallResidueZeroResidualPrintsNothing(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 6, 4, 12, 33, 0, time.UTC)
	cmd, ledger := installResidueFixture(t, now, "make", projmuxProcessVintage{
		Supported: true,
		Roles: []projmuxProcessRoleVintage{
			{Role: projmuxProcessRoleSupervisor, Processes: 3, Current: 3},
		},
	})
	var stderr bytes.Buffer
	cmd.Run(&stderr)

	if stderr.Len() != 0 {
		t.Fatalf("notice = %q, want absolutely no output when nothing was left behind", stderr.String())
	}
	records := readInstallResidueRecords(t, ledger)
	if len(records) != 1 {
		t.Fatalf("ledger records = %d, want the silent install still recorded", len(records))
	}
	if records[0].Replaced != 0 || records[0].Observed != 3 || !records[0].Supported {
		t.Fatalf("record = %+v, want a supported census with zero residual", records[0])
	}
}

// TestInstallResidueUnsupportedPlatformPrintsNothingButRecordsTheGap pins the
// darwin path.
//
// There is no /proc there, so the census cannot be taken and the operator has
// no action available; a line at every install would be permanent noise. The
// record is still written with supported:false, because the replacement design
// has to know its macOS coverage gap rather than read an absent record as an
// install that left nothing behind.
func TestInstallResidueUnsupportedPlatformPrintsNothingButRecordsTheGap(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 6, 4, 12, 33, 0, time.UTC)
	cmd, ledger := installResidueFixture(t, now, "npm", projmuxProcessVintage{})
	var stderr bytes.Buffer
	cmd.Run(&stderr)

	if stderr.Len() != 0 {
		t.Fatalf("notice = %q, want no terminal output where the census cannot be taken", stderr.String())
	}
	records := readInstallResidueRecords(t, ledger)
	if len(records) != 1 {
		t.Fatalf("ledger records = %d, want 1", len(records))
	}
	if records[0].Supported {
		t.Fatalf("supported = true, want the record to say the measurement was impossible")
	}
	if records[0].Installer != "npm" {
		t.Fatalf("installer = %q, want npm", records[0].Installer)
	}
	if len(records[0].Roles) != 0 {
		t.Fatalf("roles = %+v, want no census without a process table", records[0].Roles)
	}
}

// TestInstallResidueCarriesNoProcessIdentity is the privacy gate, asserted
// negatively over both surfaces at once.
//
// The census exists because reading /proc/<pid>/exe by hand was the only way to
// establish this, and the whole reason it is safe to print at every install is
// that it names nobody. A pid, an executable path, or one word of a caller's
// argv reaching either the terminal or the ledger would make this a diagnostic
// that has to be redacted before it is shared.
func TestInstallResidueCarriesNoProcessIdentity(t *testing.T) {
	t.Parallel()

	const (
		sentinelExe  = "/home/user/go/bin/projmux"
		sentinelArgv = "PANE-SENTINEL-ARGV"
	)
	sentinelPIDs := []int{987654, 987655, 987656}
	now := time.Date(2026, 9, 6, 4, 12, 33, 0, time.UTC)
	images := []codexProcessImage{
		{PID: 987650, Exe: sentinelExe, Cmdline: []string{sentinelExe, "doctor"}, StartedAt: now.Add(-time.Minute)},
		{
			PID: sentinelPIDs[0], Exe: sentinelExe + procDeletedSuffix,
			Cmdline:   []string{sentinelExe, "internal", "codex-broker", "serve", "--state-domain", sentinelArgv},
			StartedAt: now.Add(-2*time.Hour - 14*time.Minute),
		},
		{
			PID: sentinelPIDs[1], Exe: sentinelExe + procDeletedSuffix,
			Cmdline:   []string{sentinelExe, "internal", "supervise", "--pane-uid", sentinelArgv},
			StartedAt: now.Add(-55 * time.Minute),
		},
		{
			PID: sentinelPIDs[2], Exe: sentinelExe + procDeletedSuffix,
			Cmdline:   []string{sentinelExe, "internal", "agent-hook", "ingest", codexNativeLifecycleIngestRoute, "--pane", sentinelArgv},
			StartedAt: now.Add(-41 * time.Minute),
		},
	}
	fleet := projectProjmuxProcessVintageAt(sentinelExe, 987650, images, true, now)
	cmd, ledger := installResidueFixture(t, now, "make", fleet)
	var stderr bytes.Buffer
	cmd.Run(&stderr)

	payload, err := os.ReadFile(ledger) // #nosec G304 -- test-owned temp path.
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	for name, surface := range map[string]string{"notice": stderr.String(), "ledger": string(payload)} {
		if strings.Contains(surface, sentinelExe) {
			t.Fatalf("%s exposed the executable path: %q", name, surface)
		}
		if strings.Contains(surface, sentinelArgv) {
			t.Fatalf("%s exposed caller argv: %q", name, surface)
		}
		for _, pid := range sentinelPIDs {
			if strings.Contains(surface, strconv.Itoa(pid)) {
				t.Fatalf("%s exposed pid %d: %q", name, pid, surface)
			}
		}
	}
	if !strings.Contains(stderr.String(), "3 projmux processes are still running") {
		t.Fatalf("notice = %q, want the residual count that makes this measurement worth taking", stderr.String())
	}
}

// TestInstallResidueLedgerAppendsAndDerivesTheInstallGap pins the frequency
// half of the measurement.
//
// "How often does an install leave residue behind" is only answerable across
// records, so each new row has to land beside the old ones and carry the gap to
// the one before it. Overwriting would leave one sample and no frequency at
// all.
func TestInstallResidueLedgerAppendsAndDerivesTheInstallGap(t *testing.T) {
	t.Parallel()

	first := time.Date(2026, 9, 6, 3, 12, 21, 0, time.UTC)
	cmd, ledger := installResidueFixture(t, first, "make", residueFleet())
	cmd.Run(&bytes.Buffer{})

	cmd.now = func() time.Time { return first.Add(3612 * time.Second) }
	var stderr bytes.Buffer
	cmd.Run(&stderr)

	records := readInstallResidueRecords(t, ledger)
	if len(records) != 2 {
		t.Fatalf("ledger records = %d, want the second install appended beside the first", len(records))
	}
	if records[0].SinceLastInstallSeconds != nil {
		t.Fatalf("first record carried a gap, want it absent")
	}
	if records[1].SinceLastInstallSeconds == nil || *records[1].SinceLastInstallSeconds != 3612 {
		t.Fatalf("second record gap = %v, want 3612", records[1].SinceLastInstallSeconds)
	}
	if !strings.Contains(stderr.String(), "2 installs, last 1h00m ago") {
		t.Fatalf("notice = %q, want the ledger pointer to carry the install history", stderr.String())
	}
}

// TestInstallResidueLedgerRotatesAtItsRecordBound keeps a ledger that is
// appended to at every install from growing without end, while keeping the
// newest records, which are the ones every derived number is read from.
func TestInstallResidueLedgerRotatesAtItsRecordBound(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 6, 4, 12, 33, 0, time.UTC)
	cmd, ledger := installResidueFixture(t, now, "make", residueFleet())

	var seed bytes.Buffer
	for index := range installResidueLedgerRecords + 5 {
		fmt.Fprintf(&seed, "{\"at\":\"2026-01-01T00:00:%02dZ\",\"installer\":\"seed-%d\"}\n", index%60, index)
	}
	if err := os.WriteFile(ledger, seed.Bytes(), 0o600); err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	cmd.Run(&bytes.Buffer{})

	records := readInstallResidueRecords(t, ledger)
	if len(records) != installResidueLedgerRecords {
		t.Fatalf("ledger records = %d, want the bound of %d", len(records), installResidueLedgerRecords)
	}
	if records[len(records)-1].At != "2026-09-06T04:12:33Z" {
		t.Fatalf("newest record = %+v, want the install just recorded", records[len(records)-1])
	}
	if records[0].Installer == "seed-0" {
		t.Fatalf("oldest record = %+v, want the oldest rows dropped rather than the newest", records[0])
	}
	info, err := os.Stat(ledger)
	if err != nil {
		t.Fatalf("stat ledger: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("ledger mode = %v, want 0600 across a rotation", info.Mode().Perm())
	}
}

// TestInstallResidueNoticeOrdersRolesByResidualCount pins the render contract:
// the role with the most residual processes leads, because that is the one an
// operator acts on first, and a role with none is absent rather than printed as
// a zero.
func TestInstallResidueNoticeOrdersRolesByResidualCount(t *testing.T) {
	t.Parallel()

	notice := renderInstallResidueNotice(installResidueRecord{
		Supported: true,
		Replaced:  24,
		Roles: []projmuxProcessRoleVintage{
			{Role: codexControlPlaneRoleBroker, Processes: 1, Replaced: 1, ReplacedAgeSeconds: []int{8040}},
			{Role: codexControlPlaneRoleObserver, Processes: 3, Replaced: 3, ReplacedAgeSeconds: []int{2460, 2470, 8040}},
			{Role: projmuxProcessRoleSupervisor, Processes: 22, Replaced: 20, ReplacedAgeSeconds: []int{
				60, 120, 180, 240, 300, 600, 900, 1200, 1800, 2400,
				3000, 3300, 3600, 4200, 4800, 5400, 6000, 7200, 9000, 10920,
			}},
			// A role the install did reach. It has nothing to act on.
			{Role: projmuxProcessRoleOther, Processes: 4, Current: 4},
		},
	}, "")

	if strings.Contains(notice, projmuxProcessRoleOther) {
		t.Fatalf("notice = %q, want a role with no residual process omitted entirely", notice)
	}
	order := []string{projmuxProcessRoleSupervisor, codexControlPlaneRoleObserver, codexControlPlaneRoleBroker}
	previous := -1
	for _, role := range order {
		index := strings.Index(notice, role)
		if index < 0 {
			t.Fatalf("notice = %q, want it to name %q", notice, role)
		}
		if index < previous {
			t.Fatalf("notice = %q, want roles ordered by residual count descending", notice)
		}
		previous = index
	}
	if strings.Contains(notice, "Recorded to") {
		t.Fatalf("notice = %q, want no ledger pointer when nothing was recorded", notice)
	}
}

// TestInstallResidueNoticeTiesBreakOnTheCensusRoleOrder pins that two roles
// with the same residual count render in the census's own order rather than in
// whatever order the map iteration produced.
func TestInstallResidueNoticeTiesBreakOnTheCensusRoleOrder(t *testing.T) {
	t.Parallel()

	rows := installResidueRows([]projmuxProcessRoleVintage{
		{Role: projmuxProcessRoleSupervisor, Processes: 2, Replaced: 2},
		{Role: codexControlPlaneRoleObserver, Processes: 2, Replaced: 2},
		{Role: codexControlPlaneRoleBroker, Processes: 2, Replaced: 2},
	})
	got := []string{rows[0].Role, rows[1].Role, rows[2].Role}
	want := []string{codexControlPlaneRoleBroker, codexControlPlaneRoleObserver, projmuxProcessRoleSupervisor}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("tie order = %v, want %v", got, want)
		}
	}
}

// TestInstallResidueAgeTextRendersDurationsAnOperatorReads pins the format the
// oldest and median columns are read in, including the unknown case, which must
// not render as a zero age.
func TestInstallResidueAgeTextRendersDurationsAnOperatorReads(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		seconds int64
		want    string
	}{
		{seconds: 0, want: "0s"},
		{seconds: 41, want: "41s"},
		{seconds: 59, want: "59s"},
		{seconds: 60, want: "1m"},
		{seconds: 3300, want: "55m"},
		{seconds: 3599, want: "59m"},
		{seconds: 3600, want: "1h00m"},
		{seconds: 10920, want: "3h02m"},
		{seconds: -5, want: "0s"},
	} {
		if got := formatInstallResidueAge(test.seconds); got != test.want {
			t.Fatalf("formatInstallResidueAge(%d) = %q, want %q", test.seconds, got, test.want)
		}
	}
	if got := installResidueAgeText(projmuxProcessRoleVintage{Role: "supervisor", Replaced: 2}); got != "age unknown" {
		t.Fatalf("age text with no samples = %q, want it to say the ages are unknown", got)
	}
	partial := installResidueAgeText(projmuxProcessRoleVintage{Role: "supervisor", Replaced: 4, ReplacedAgeSeconds: []int{60, 120}})
	if !strings.Contains(partial, "2 of 4 timed") {
		t.Fatalf("partial age text = %q, want it to say how much of the role it timed", partial)
	}
	if got := installResidueMedian([]int{610, 1200, 4300, 8800}); got != 2750 {
		t.Fatalf("median of an even distribution = %d, want 2750", got)
	}
	if got := installResidueMedian([]int{610, 1200, 4300}); got != 1200 {
		t.Fatalf("median of an odd distribution = %d, want 1200", got)
	}
}

// TestInstallResidueNeverFailsAnInstall is acceptance's hard edge.
//
// This runs as the last step of an install that has already succeeded. A
// diagnostic that can turn a completed install into a failed one is worse than
// no diagnostic at all, so every failure path here -- an unresolvable state
// directory, an unwritable ledger -- has to end in a silent exit 0.
func TestInstallResidueNeverFailsAnInstall(t *testing.T) {
	t.Parallel()

	t.Run("unresolvable state directory", func(t *testing.T) {
		cmd := &installResidueCommand{
			now:         time.Now,
			getenv:      func(string) string { return "" },
			stateDir:    func() (string, error) { return "", os.ErrNotExist },
			readVintage: func(time.Time) projmuxProcessVintage { return residueFleet() },
		}
		var stderr bytes.Buffer
		cmd.Run(&stderr)
		if strings.Contains(stderr.String(), "Recorded to") {
			t.Fatalf("notice = %q, want no ledger pointer when nothing could be recorded", stderr.String())
		}
		if !strings.Contains(stderr.String(), "still running the image this install replaced") {
			t.Fatalf("notice = %q, want the census still reported without a ledger", stderr.String())
		}
	})

	t.Run("unwritable ledger", func(t *testing.T) {
		dir := t.TempDir()
		blocked := filepath.Join(dir, "blocked")
		if err := os.WriteFile(blocked, []byte("not a directory\n"), 0o600); err != nil {
			t.Fatalf("seed blocker: %v", err)
		}
		cmd := &installResidueCommand{
			now:         time.Now,
			getenv:      func(string) string { return "" },
			stateDir:    func() (string, error) { return blocked, nil },
			readVintage: func(time.Time) projmuxProcessVintage { return residueFleet() },
		}
		cmd.Run(&bytes.Buffer{})
	})

	t.Run("route always exits zero", func(t *testing.T) {
		if err := runInstallResidueReport(nil, nil, nil); err != nil {
			t.Fatalf("runInstallResidueReport() error = %v, want nil on every path", err)
		}
	})
}

// TestInstallResidueInstallerComesFromTheExistingEnvVar pins that the installer
// identity is read from PROJMUX_INSTALLER rather than from a new variable. Any
// new PROJMUX_* env var is a minor-release input to the hook contract, and this
// route needs no such input.
func TestInstallResidueInstallerComesFromTheExistingEnvVar(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		env  string
		want string
	}{
		{env: "make", want: "make"},
		{env: "npm", want: "npm"},
		{env: "  github-release  ", want: "github-release"},
		{env: "", want: installerUnknown},
	} {
		got := installResidueInstaller(func(name string) string {
			if name != "PROJMUX_INSTALLER" {
				t.Fatalf("read env %q, want only PROJMUX_INSTALLER", name)
			}
			return test.env
		})
		if got != test.want {
			t.Fatalf("installer for %q = %q, want %q", test.env, got, test.want)
		}
	}
	if got := installResidueInstaller(nil); got != installerUnknown {
		t.Fatalf("installer with no environment = %q, want %q", got, installerUnknown)
	}
}

// TestInstallResidueRouteIsRegisteredAsInternalPlumbing pins that the census is
// reachable at the spelling `make install` and the npm wrapper invoke, and that
// it stays hidden internal plumbing rather than a public command.
func TestInstallResidueRouteIsRegisteredAsInternalPlumbing(t *testing.T) {
	t.Parallel()

	if !containsRoute(internalSubcommands, "install-residue") {
		t.Fatalf("internal subcommands = %v, want install-residue", internalSubcommands)
	}
	if shouldRunLegacyHookMigrations([]string{"internal", "install-residue"}) {
		t.Fatal("the install residue census triggered the legacy hook migration; it is a read-and-record route")
	}
}
