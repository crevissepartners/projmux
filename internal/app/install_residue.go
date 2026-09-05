package app

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

// The install residue census: how much of the running fleet an install did not
// reach, measured at the moment the install finished.
//
// `make install` and `npm install` replace the executable on disk. Neither
// replaces the image of a process that is already running, so every long-lived
// projmux child keeps executing the code it started with until it exits on its
// own. The operator is never told, and `projmux doctor` is a place they have no
// reason to look right after an install that reported success.
//
// This route is the measurement, not the message. Three numbers have to survive
// it, because a later automatic-replacement design cannot choose a drain
// termination condition without them: how many residual processes there are per
// role, how old they are as a distribution, and how often an install leaves any
// behind at all. The terminal line is a courtesy; the ledger is the deliverable.
//
// Nothing in this file terminates, starts, signals, or restarts a process. The
// census is two file reads per process and the report is text.
const (
	// installResidueLedgerFile is the append ledger, under the projmux state
	// directory alongside the other JSON Lines journals.
	installResidueLedgerFile = "install-residue.jsonl"
	// installResidueLedgerRecords bounds how many install records the ledger
	// keeps. Frequency is read across records, so the history has to be long
	// enough to carry many installs and bounded so it cannot grow without end.
	installResidueLedgerRecords = 1000
	// installResidueLedgerReadLimit bounds one ledger read. A ledger larger
	// than this is read from its tail, which is the end every derived number
	// comes from anyway.
	installResidueLedgerReadLimit = 8 << 20
	// installResidueLedgerLineLimit bounds one ledger row while scanning.
	installResidueLedgerLineLimit = 1 << 20
)

// installResidueRecord is one install's census, as one JSON Lines row.
//
// It carries counts and durations only. No pid, no executable path, no argv,
// and no provider content reaches this file: process identity is exactly what
// the census is built to avoid recording, and the fields below are the whole
// record.
type installResidueRecord struct {
	// At is when the census was taken, in RFC3339 UTC. Across records this is
	// how often an install leaves residue behind.
	At string `json:"at"`
	// Installer is the install path that produced this record, from the
	// existing PROJMUX_INSTALLER env var. Unset reads as "unknown".
	Installer string `json:"installer"`
	// Supported reports whether this platform exposes a process table the
	// census can be taken from. A false record is the measurement's own
	// coverage gap, recorded rather than omitted.
	Supported bool `json:"supported"`
	// Observed is how many projmux processes the census classified.
	Observed int `json:"observed"`
	// Replaced is how many of them run the image this install replaced.
	Replaced int `json:"replaced"`
	// SinceLastInstallSeconds is the gap to the previous record, absent on the
	// first one.
	SinceLastInstallSeconds *int64 `json:"sinceLastInstallSeconds,omitempty"`
	// Roles is the per-role census, including each role's residual age
	// distribution.
	Roles []projmuxProcessRoleVintage `json:"roles,omitempty"`
}

const installerUnknown = "unknown"

// installResidueCommand is the hidden `internal install-residue` route.
//
// Every dependency is injected so the whole report -- census, ledger, and
// rendered text -- is exercised without a process table, a clock, or the real
// state directory.
type installResidueCommand struct {
	now         func() time.Time
	getenv      func(string) string
	stateDir    func() (string, error)
	readVintage func(now time.Time) projmuxProcessVintage
}

func newInstallResidueCommand() *installResidueCommand {
	return &installResidueCommand{
		now:    time.Now,
		getenv: os.Getenv,
		stateDir: func() (string, error) {
			paths, err := config.DefaultPathsFromEnv()
			if err != nil {
				return "", err
			}
			return paths.StateDir, nil
		},
		readVintage: defaultInstallResidueVintage,
	}
}

// runInstallResidueReport is the route entrypoint.
//
// It always returns nil. This runs as the last step of an install that has
// already succeeded, and a diagnostic that can fail the thing it reports on is
// worse than no diagnostic: an unwritable state directory must not turn a
// completed install into a failed one.
func runInstallResidueReport(_ []string, _ io.Writer, stderr io.Writer) error {
	newInstallResidueCommand().Run(stderr)
	return nil
}

// Run takes the census, appends the ledger record, and renders the notice.
//
// Every failure is swallowed on purpose; see runInstallResidueReport.
func (c *installResidueCommand) Run(stderr io.Writer) {
	if c == nil {
		return
	}
	now := time.Now()
	if c.now != nil {
		now = c.now()
	}
	now = now.UTC()

	vintage := projmuxProcessVintage{}
	if c.readVintage != nil {
		vintage = c.readVintage(now)
	}

	record := installResidueRecord{
		At:        now.Format(time.RFC3339),
		Installer: installResidueInstaller(c.getenv),
		Supported: vintage.Supported,
		Observed:  vintage.Observed(),
		Replaced:  vintage.Replaced(),
		Roles:     vintage.Roles,
	}

	path := c.ledgerPath()
	previous, existing := readInstallResidueLedger(path)
	if !previous.IsZero() {
		seconds := max(int64(now.Sub(previous)/time.Second), 0)
		record.SinceLastInstallSeconds = &seconds
	}
	written := appendInstallResidueRecord(path, record)

	pointer := ""
	if written {
		pointer = renderInstallResiduePointer(path, existing+1, record.SinceLastInstallSeconds)
	}
	// An unsupported platform prints nothing. The operator there has no action
	// available and no way to take the census, so a line at every install would
	// be permanent noise; the ledger still records that the measurement was
	// attempted and was impossible, which is the macOS coverage gap a later
	// replacement design has to know about.
	if !record.Supported || record.Replaced == 0 || stderr == nil {
		return
	}
	if text := renderInstallResidueNotice(record, pointer); text != "" {
		_, _ = io.WriteString(stderr, text)
	}
}

func (c *installResidueCommand) ledgerPath() string {
	if c.stateDir == nil {
		return ""
	}
	dir, err := c.stateDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, installResidueLedgerFile)
}

// installResidueInstaller names the install path that produced this record.
//
// It reads the existing PROJMUX_INSTALLER variable rather than introducing a
// new one: the hook contract makes any new PROJMUX_* env var a minor-release
// input, and this variable already carries exactly this fact.
func installResidueInstaller(getenv func(string) string) string {
	if getenv == nil {
		return installerUnknown
	}
	if installer := strings.TrimSpace(getenv("PROJMUX_INSTALLER")); installer != "" {
		return installer
	}
	return installerUnknown
}

// defaultInstallResidueVintage takes one process-table read and projects the
// whole-fleet census from it, with residual ages measured against now.
//
// A platform with no readable process table, and an executable this process
// cannot resolve, both report unsupported. Neither can produce a census, and
// reporting one of them as an empty fleet would claim an install left nothing
// behind on evidence that says nothing at all.
func defaultInstallResidueVintage(now time.Time) projmuxProcessVintage {
	executable, err := os.Executable()
	if err != nil {
		return projmuxProcessVintage{}
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		resolved = executable
	}
	images, supported := defaultCodexProcessImages()
	return projectProjmuxProcessVintageAt(resolved, os.Getpid(), images, supported, now)
}

// readInstallResidueLedger reports the previous record's instant and how many
// records the ledger already holds.
//
// A missing, unreadable, or malformed ledger reads as no history rather than as
// an error: the record about to be written is still worth keeping, and the only
// thing lost is one derived gap.
func readInstallResidueLedger(path string) (time.Time, int) {
	if strings.TrimSpace(path) == "" {
		return time.Time{}, 0
	}
	lines, ok := readInstallResidueLedgerLines(path)
	if !ok {
		return time.Time{}, 0
	}
	previous := time.Time{}
	for index := len(lines) - 1; index >= 0; index-- {
		var record installResidueRecord
		if err := json.Unmarshal(lines[index], &record); err != nil {
			continue
		}
		at, err := time.Parse(time.RFC3339, strings.TrimSpace(record.At))
		if err != nil {
			continue
		}
		previous = at
		break
	}
	return previous, len(lines)
}

// readInstallResidueLedgerLines reads the ledger's rows, from its tail when the
// file is larger than one bounded read.
func readInstallResidueLedgerLines(path string) ([][]byte, bool) {
	// #nosec G304 -- the path is resolved from projmux's own state directory.
	file, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, false
	}
	partial := false
	if info.Size() > installResidueLedgerReadLimit {
		if _, err := file.Seek(info.Size()-installResidueLedgerReadLimit, io.SeekStart); err != nil {
			return nil, false
		}
		partial = true
	}
	var lines [][]byte
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), installResidueLedgerLineLimit)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		lines = append(lines, append([]byte(nil), line...))
	}
	if err := scanner.Err(); err != nil {
		return nil, false
	}
	if partial && len(lines) > 0 {
		// The first row of a tail read may have been cut mid-line.
		lines = lines[1:]
	}
	return lines, true
}

// appendInstallResidueRecord places one row and reports whether it landed.
//
// The common path is a single O_APPEND write. The ledger is rewritten only when
// it has grown past its bound, and that rewrite goes through a temp file and a
// rename so a failed trim cannot leave a truncated history behind.
func appendInstallResidueRecord(path string, record installResidueRecord) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	body, err := json.Marshal(record)
	if err != nil {
		return false
	}
	if err := localstate.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return false
	}
	// #nosec G304 -- the path is resolved from projmux's own state directory.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, localstate.PrivateFileMode)
	if err != nil {
		return false
	}
	written := true
	if _, err := file.Write(append(body, '\n')); err != nil {
		written = false
	}
	if err := file.Close(); err != nil {
		written = false
	}
	if written {
		trimInstallResidueLedger(path)
	}
	return written
}

// trimInstallResidueLedger drops the oldest rows once the ledger is over its
// record bound. A failure here leaves the ledger longer than the bound, which
// is strictly better than leaving it damaged.
func trimInstallResidueLedger(path string) {
	lines, ok := readInstallResidueLedgerLines(path)
	if !ok || len(lines) <= installResidueLedgerRecords {
		return
	}
	kept := lines[len(lines)-installResidueLedgerRecords:]
	var buf bytes.Buffer
	for _, line := range kept {
		buf.Write(line)
		buf.WriteByte('\n')
	}
	temp := path + ".trim"
	// #nosec G306 -- localstate.PrivateFileMode is 0600.
	if err := os.WriteFile(temp, buf.Bytes(), localstate.PrivateFileMode); err != nil {
		_ = os.Remove(temp)
		return
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
	}
}

// renderInstallResiduePointer names the ledger and summarizes its history.
func renderInstallResiduePointer(path string, records int, since *int64) string {
	installs := "1 install"
	if records != 1 {
		installs = fmt.Sprintf("%d installs", records)
	}
	if since == nil {
		return fmt.Sprintf("%s (%s recorded)", path, installs)
	}
	return fmt.Sprintf("%s (%s, last %s ago)", path, installs, formatInstallResidueAge(*since))
}

// renderInstallResidueNotice writes the residual census as the last thing an
// install prints.
//
// Roles are ordered by residual count descending so the biggest one leads, with
// the census's own role order breaking ties. A role with no residual process is
// omitted entirely rather than printed as a zero: a zero row is a line an
// operator has to read to learn there was nothing to read.
func renderInstallResidueNotice(record installResidueRecord, pointer string) string {
	rows := installResidueRows(record.Roles)
	if len(rows) == 0 {
		return ""
	}
	width := 0
	count := 0
	for _, row := range rows {
		if len(row.Role) > width {
			width = len(row.Role)
		}
		if digits := len(fmt.Sprintf("%d", row.Replaced)); digits > count {
			count = digits
		}
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, ">> %d projmux %s still running the image this install replaced\n",
		record.Replaced, pluralizeInstallResidueProcesses(record.Replaced))
	for _, row := range rows {
		fmt.Fprintf(&buf, "     %-*s  %*d   %s\n", width, row.Role, count, row.Replaced, installResidueAgeText(row))
	}
	buf.WriteString("   They keep executing code from before this install until each one exits.\n")
	buf.WriteString("   Recreating a pane moves that pane onto the installed build; the broker\n")
	buf.WriteString("   follows when its last binding goes.\n")
	if pointer != "" {
		fmt.Fprintf(&buf, "   Recorded to %s.\n", pointer)
	}
	return buf.String()
}

func pluralizeInstallResidueProcesses(count int) string {
	if count == 1 {
		return "process is"
	}
	return "processes are"
}

// installResidueRows selects the roles with residual processes, biggest first.
func installResidueRows(roles []projmuxProcessRoleVintage) []projmuxProcessRoleVintage {
	var rows []projmuxProcessRoleVintage
	for _, role := range roles {
		if role.Replaced > 0 {
			rows = append(rows, role)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Replaced != rows[j].Replaced {
			return rows[i].Replaced > rows[j].Replaced
		}
		return projmuxProcessRoleRank(rows[i].Role) < projmuxProcessRoleRank(rows[j].Role)
	})
	return rows
}

// projmuxProcessRoleRank is the census's own role order, used to break a tie in
// residual count.
func projmuxProcessRoleRank(role string) int {
	for index, known := range projmuxProcessRoleOrder {
		if known == role {
			return index
		}
	}
	return len(projmuxProcessRoleOrder)
}

// installResidueAgeText renders one role's oldest and median residual age.
//
// A role whose start times were all unreadable says so rather than rendering a
// zero, which would read as processes that had just started.
func installResidueAgeText(role projmuxProcessRoleVintage) string {
	ages := role.ReplacedAgeSeconds
	if len(ages) == 0 {
		return "age unknown"
	}
	oldest := ages[len(ages)-1]
	text := fmt.Sprintf("oldest %s   median %s",
		formatInstallResidueAge(int64(oldest)), formatInstallResidueAge(int64(installResidueMedian(ages))))
	if role.ReplacedAgeCapped || len(ages) < role.Replaced {
		text += fmt.Sprintf("   (%d of %d timed)", len(ages), role.Replaced)
	}
	return text
}

// installResidueMedian is the median of an ascending age distribution.
func installResidueMedian(ages []int) int {
	if len(ages) == 0 {
		return 0
	}
	middle := len(ages) / 2
	if len(ages)%2 == 1 {
		return ages[middle]
	}
	return (ages[middle-1] + ages[middle]) / 2
}

// formatInstallResidueAge renders a whole-second age the way an operator reads
// one: seconds under a minute, minutes under an hour, hours and minutes above.
func formatInstallResidueAge(seconds int64) string {
	if seconds < 0 {
		seconds = 0
	}
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := seconds / 60
	if minutes < 60 {
		return fmt.Sprintf("%dm", minutes)
	}
	return fmt.Sprintf("%dh%02dm", minutes/60, minutes%60)
}
