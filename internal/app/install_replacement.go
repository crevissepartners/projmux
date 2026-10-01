package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

// The install-side replacement pass: the step that makes an install finish the
// replacement it starts.
//
// Publishing a new executable does not replace the image of a process already
// running the old one. Until now the install said so and stopped there -- the
// residue census counted what was left behind and printed a notice. This route
// is the other half: for the roles the policy table marks drainable, it asks
// through the path this application already ships, waits a bounded moment for
// the answer, and records the outcome where doctor's L2 row reads it.
//
// Three things it deliberately does not do. It starts no runtime -- a
// replacement pass that launched what it was sent to replace would be a way to
// leave more behind than it found. It signals nothing: the only request it
// makes is a socket handshake, and the runtime decides when to go. And it
// touches no role the policy table marks report-only, which is what keeps the
// operator's own attached session out of its reach.
const (
	// installReplacementFile is the last pass's outcome, under the projmux
	// state directory beside the residue ledger.
	//
	// One record rather than a journal: the residue ledger answers "how often
	// does an install leave residue", which needs history, while this answers
	// "what did the last install's replacement pass do", which is a question
	// about now. A verdict expires when the installed binary changes, so a
	// pass older than the current install has nothing to say.
	installReplacementFile = "install-replacement.json"
	// installReplacementReadLimit bounds the outcome read.
	installReplacementReadLimit = 64 << 10
	// installReplacementDialTimeout bounds one drain request.
	//
	// The request is a local socket handshake against a runtime that is either
	// there or not. Two seconds is far above the observed cost and low enough
	// that an unreachable socket cannot hold an install open.
	installReplacementDialTimeout = 2 * time.Second
	// installReplacementSettle bounds the wait for a requested drain to
	// finish.
	//
	// This is not the cutoff. The cutoff is the bound on the replacement; this
	// is the bound on how long the install stands there watching. An idle
	// runtime closes as soon as it drains, so this only has to cover that, and
	// a runtime with live bindings is reported as pending rather than waited
	// on -- the install has no business blocking on somebody else's work.
	installReplacementSettle = 3 * time.Second
	// installReplacementPoll paces the settle wait.
	installReplacementPoll = 50 * time.Millisecond
)

// The closed outcome vocabulary of one replacement pass.
//
// Every token names what the pass did, not what it found: the census already
// reports what was found, and a pass that reported the same thing twice in
// different words would give a reader two facts to reconcile instead of one.
const (
	// installReplacementOutcomeUnsupported is a platform with no process table
	// to take a census from. Nothing is asked, because nothing can be named.
	installReplacementOutcomeUnsupported = "replacement-unsupported-platform"
	// installReplacementOutcomeNoTarget is a fleet with no residual process in
	// a drainable role. Report-only residue may still be present; it is
	// counted on the row and left alone.
	installReplacementOutcomeNoTarget = "replacement-no-target"
	// installReplacementOutcomeComplete is a drain that was asked for and
	// finished inside the settle window.
	installReplacementOutcomeComplete = "replacement-complete"
	// installReplacementOutcomePending is a drain that was accepted and had
	// not finished when the pass stopped watching. The runtime is carrying
	// live work; it accepts no new work and goes when that work ends.
	installReplacementOutcomePending = "replacement-drain-pending"
	// installReplacementOutcomeUnreachable is a residual target the shipped
	// path could not reach. The refusal that says why travels with it.
	installReplacementOutcomeUnreachable = "replacement-target-unreachable"
	// installReplacementOutcomeNotAttempted is what a reader reports when no
	// pass record exists at all. No pass writes it.
	installReplacementOutcomeNotAttempted = "replacement-not-attempted"
)

// installReplacementOutcome is one pass, as one JSON object.
//
// Like the residue ledger it carries counts and closed tokens only. No pid, no
// executable path, no argv, and no socket path reaches this file.
type installReplacementOutcome struct {
	// At is when the pass ran, in RFC3339 UTC.
	At string `json:"at"`
	// Installer is the install path that ran it, from the existing
	// PROJMUX_INSTALLER env var. Unset reads as "unknown".
	Installer string `json:"installer"`
	// Supported reports whether the census this pass is built on could be
	// taken at all.
	Supported bool `json:"supported"`
	// Outcome is the closed token above.
	Outcome string `json:"outcome"`
	// CutoffSeconds is the drain cutoff this pass ran under.
	CutoffSeconds int64 `json:"cutoffSeconds"`
	// Attempted is how many residual processes sat in a drainable role.
	Attempted int `json:"attempted"`
	// Drained is how many of them were gone when the pass stopped watching.
	Drained int `json:"drained"`
	// Reported is how many residual processes the policy table left alone.
	Reported int `json:"reported"`
	// BeyondCutoff is how many residual processes had already outlived the
	// cutoff when the pass ran.
	BeyondCutoff int `json:"beyondCutoff"`
	// Refusal is the underlying broker refusal token, when the request was
	// refused. It is the discriminant C-3 requires: `replacement-complete` and
	// `replacement-target-unreachable` are verdicts, and only this says which
	// door was closed.
	Refusal string `json:"refusal,omitempty"`
	// FailureStage distinguishes discovery, socket dial, and handshake failures.
	FailureStage string `json:"failureStage,omitempty"`
}

// installReplacementCommand is the hidden `internal install-replace` route.
//
// Every dependency is injected so the pass -- policy, request, settle, and
// record -- is exercised without a process table, a broker, a clock, or the
// real state directory.
type installReplacementCommand struct {
	now         func() time.Time
	getenv      func(string) string
	stateDir    func() (string, error)
	readVintage func(now time.Time) projmuxProcessVintage
	// readTargets is a fresh, output-only observation after a failed request.
	// Process identities never enter the persisted outcome or residue census.
	readTargets func() []installReplacementTarget
	locale      i18n.Locale
	// requestDrain captures the published targets and returns a counter that
	// observes only those exact sockets, even if a successor is published.
	requestDrain func(ctx context.Context) installReplacementDrainResult
	settle       time.Duration
	poll         time.Duration
}

func newInstallReplacementCommand() *installReplacementCommand {
	command := &installReplacementCommand{
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
		readTargets: defaultInstallReplacementTargets,
		locale:      appLocale(os.UserHomeDir, os.Getenv),
		settle:      installReplacementSettle,
		poll:        installReplacementPoll,
	}
	command.requestDrain = defaultInstallReplacementDrainRequest
	return command
}

// runInstallReplacement is the route entrypoint.
//
// Binary publication and config convergence have already finished. An
// unreachable replacement target still fails this step, without rolling either
// of those completed stages back. Accepted drains carrying work remain success.
func runInstallReplacement(args []string, stderr io.Writer) error {
	if len(args) != 0 {
		return usageError("internal install-replace does not accept arguments")
	}
	return newInstallReplacementCommand().Run(stderr)
}

// Run takes the census, asks the drainable roles to stand down, waits a bounded
// moment, and records what happened.
func (c *installReplacementCommand) Run(stderr io.Writer) error {
	if c == nil {
		return nil
	}
	now := time.Now()
	if c.now != nil {
		now = c.now()
	}
	now = now.UTC()
	cutoff := resolveReplacementCutoff(c.getenv)

	outcome := installReplacementOutcome{
		At:            now.Format(time.RFC3339),
		Installer:     installResidueInstaller(c.getenv),
		CutoffSeconds: int64(cutoff / time.Second),
		Outcome:       installReplacementOutcomeUnsupported,
	}

	vintage := projmuxProcessVintage{}
	if c.readVintage != nil {
		vintage = c.readVintage(now)
	}
	outcome.Supported = vintage.Supported
	if vintage.Supported {
		outcome.Attempted, outcome.Reported = replacementResidualByDisposition(vintage.Roles)
		outcome.BeyondCutoff = replacementResidualBeyondCutoff(vintage.Roles, cutoff)
		c.replace(&outcome)
	}

	c.write(outcome)
	if text := renderInstallReplacementNotice(outcome); text != "" && stderr != nil {
		_, _ = io.WriteString(stderr, text)
	}
	if outcome.Outcome == installReplacementOutcomeUnreachable {
		var targets []installReplacementTarget
		if c.readTargets != nil {
			targets = c.readTargets()
		}
		if stderr != nil {
			_, _ = io.WriteString(stderr, renderInstallReplacementFailure(targets, c.locale))
		}
		return installReplacementExitError{}
	}
	return nil
}

// The diagnostic has already been printed. The CLI's exitCoder contract keeps
// the failure machine-readable without printing a second, generic error line.
type installReplacementExitError struct{}

func (installReplacementExitError) Error() string { return installReplacementOutcomeUnreachable }
func (installReplacementExitError) ExitCode() int { return 1 }

// replace asks the exact residual targets for this pass and watches for
// their answers.
func (c *installReplacementCommand) replace(outcome *installReplacementOutcome) {
	if outcome.Attempted == 0 {
		outcome.Outcome = installReplacementOutcomeNoTarget
		return
	}
	if c.requestDrain == nil {
		outcome.FailureStage = string(codexbroker.DialStageDiscovery)
		outcome.Outcome = installReplacementOutcomeUnreachable
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), installReplacementDialTimeout)
	defer cancel()
	result := c.requestDrain(ctx)
	// The target snapshot used for selection is fresher than the role census.
	// A target may have exited between them; counts must follow the same exact
	// snapshot the request used, never turn that normal absence into failure.
	outcome.Attempted = result.attempted
	if result.attempted == 0 && result.failureStage == "" {
		outcome.Outcome = installReplacementOutcomeNoTarget
		return
	}
	outcome.Refusal = result.refusal
	outcome.FailureStage = result.failureStage
	outcome.Drained = c.settleDrained(result)
	if result.accepted < outcome.Attempted || result.failureStage != "" {
		// A welcome from another, current runtime is not an accepted drain,
		// and fewer published targets cannot stand in for the whole census.
		if outcome.FailureStage == "" {
			outcome.FailureStage = string(codexbroker.DialStageDiscovery)
			outcome.Refusal = string(codexbroker.RefusalHostUnavailable)
		}
		outcome.Outcome = installReplacementOutcomeUnreachable
		return
	}
	if outcome.Drained == outcome.Attempted {
		outcome.Outcome = installReplacementOutcomeComplete
		return
	}
	outcome.Outcome = installReplacementOutcomePending
}

type installReplacementDrainResult struct {
	attempted    int
	accepted     int
	refusal      string
	failureStage string
	// drained observes the socket identities captured before the requests.
	drained func() int
}

// settleDrained counts only accepted targets that have disappeared. A successor
// at the same path neither hides completion nor gets counted as a drained host.
func (c *installReplacementCommand) settleDrained(result installReplacementDrainResult) int {
	if result.drained == nil || result.accepted == 0 {
		return 0
	}
	poll := c.poll
	if poll <= 0 {
		poll = installReplacementPoll
	}
	deadline := time.Now().Add(c.settle)
	for {
		drained := result.drained()
		if drained == result.accepted || !time.Now().Before(deadline) {
			return drained
		}
		time.Sleep(poll)
	}
}

// write places the outcome record. An unwritable diagnostic record does not
// change the replacement result or hide its terminal diagnostic.
func (c *installReplacementCommand) write(outcome installReplacementOutcome) {
	path := c.recordPath()
	if strings.TrimSpace(path) == "" {
		return
	}
	body, err := json.Marshal(outcome)
	if err != nil {
		return
	}
	if err := localstate.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return
	}
	temp := path + ".tmp"
	// #nosec G306 -- localstate.PrivateFileMode is 0600.
	if err := os.WriteFile(temp, append(body, '\n'), localstate.PrivateFileMode); err != nil {
		_ = os.Remove(temp)
		return
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
	}
}

func (c *installReplacementCommand) recordPath() string {
	if c.stateDir == nil {
		return ""
	}
	dir, err := c.stateDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, installReplacementFile)
}

// readInstallReplacementOutcome reads the last pass's record.
//
// A missing, unreadable, or malformed record reads as no pass rather than as an
// error. The L2 row still has the live census to reach a verdict from; what it
// loses is only the account of what the last install tried.
func readInstallReplacementOutcome(path string) (installReplacementOutcome, bool) {
	if strings.TrimSpace(path) == "" {
		return installReplacementOutcome{}, false
	}
	// #nosec G304 -- the path is resolved from projmux's own state directory.
	file, err := os.Open(path)
	if err != nil {
		return installReplacementOutcome{}, false
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, installReplacementReadLimit))
	if err != nil {
		return installReplacementOutcome{}, false
	}
	var outcome installReplacementOutcome
	if json.Unmarshal(payload, &outcome) != nil || strings.TrimSpace(outcome.Outcome) == "" {
		return installReplacementOutcome{}, false
	}
	return outcome, true
}

// renderInstallReplacementNotice writes what the pass did, and nothing when it
// had nothing to do.
//
// A pass that found no target prints nothing: it ran as one line of an install
// that already succeeded, and "there was nothing to replace" is not news an
// operator has to read at every install. The two lines that do print are the
// ones an action follows from.
func renderInstallReplacementNotice(outcome installReplacementOutcome) string {
	switch outcome.Outcome {
	case installReplacementOutcomeComplete:
		return fmt.Sprintf(">> replaced %d long-lived %s running the image this install superseded\n",
			outcome.Drained, pluralizeInstallReplacementProcesses(outcome.Drained))
	case installReplacementOutcomePending:
		return fmt.Sprintf(">> asked %d long-lived %s to stand down; %s still carrying work\n"+
			"   The runtime accepts no new work and goes when that work ends.\n",
			outcome.Attempted, pluralizeInstallReplacementProcesses(outcome.Attempted),
			pluralizeInstallReplacementSubject(outcome.Attempted))
	case installReplacementOutcomeUnreachable:
		refusal := strings.TrimSpace(outcome.Refusal)
		if refusal == "" {
			refusal = "unknown"
		}
		return fmt.Sprintf(">> could not reach %d long-lived %s to replace: %s\n",
			outcome.Attempted, pluralizeInstallReplacementProcesses(outcome.Attempted), refusal)
	default:
		return ""
	}
}

// pluralizeInstallReplacementProcesses is the bare noun. The residue census's
// pluralizer carries its own verb ("process is"), which these sentences already
// supply.
func pluralizeInstallReplacementProcesses(count int) string {
	if count == 1 {
		return "process"
	}
	return "processes"
}

func pluralizeInstallReplacementSubject(count int) string {
	if count == 1 {
		return "it is"
	}
	return "they are"
}

// defaultInstallReplacementDrainRequest discovers the generation-scoped
// endpoints already published in this state domain. The legacy default key is
// a directory locator, not the key a managed Agent's broker publishes.
func defaultInstallReplacementDrainRequest(ctx context.Context) installReplacementDrainResult {
	residual := readInstallReplacementTargets(nil, nil)
	if len(residual) == 0 {
		return installReplacementDrainResult{}
	}
	domain, err := codexBrokerStateDomain(os.Getenv, os.UserHomeDir)
	if err != nil {
		return installReplacementDrainResult{attempted: len(residual), refusal: string(codexbroker.RefusalDomainRequired), failureStage: string(codexbroker.DialStageDiscovery)}
	}
	return requestInstallReplacementDrain(ctx, domain, residual)
}

func requestInstallReplacementDrain(ctx context.Context, domain string, residual []installReplacementTarget) installReplacementDrainResult {
	result := installReplacementDrainResult{attempted: len(residual)}
	if len(residual) == 0 {
		return result
	}
	published, refusal := codexBrokerPublishedRuntimes(domain)
	fail := func(stage, reason string) {
		if result.failureStage == "" {
			result.failureStage, result.refusal = stage, reason
		}
	}
	if refusal != "" || len(published) == 0 {
		if refusal == "" {
			refusal = string(codexbroker.RefusalHostUnavailable)
		}
		fail(string(codexbroker.DialStageDiscovery), refusal)
		return result
	}
	// PID only selects records that describe this executable's residual fleet.
	// It grants no authority: Dial still proves ownership and authenticates the
	// record's endpoint and credential, and completion uses the socket inode.
	wanted := make(map[int]bool, len(residual))
	for _, target := range residual {
		wanted[target.pid] = true
	}
	var targets []installReplacementSocket
	welcomed := false
	for _, discovery := range published {
		pid := installReplacementRecordPID(discovery.RecordPath())
		if !wanted[pid] {
			continue
		}
		delete(wanted, pid)
		runtimeID, err := codexbroker.PublishedRuntimeID(discovery)
		if err != nil {
			fail(string(codexbroker.DialStageDiscovery), string(codexbroker.RefusalOf(err)))
			continue
		}
		info, err := os.Lstat(discovery.SocketPath())
		if err != nil {
			fail(string(codexbroker.DialStageDiscovery), string(codexbroker.RefusalHostUnavailable))
			continue
		}
		target := installReplacementSocket{path: discovery.SocketPath(), info: info, discovery: discovery, runtime: runtimeID}
		conn, err := codexbroker.Dial(ctx, discovery, codexbroker.DialConfig{Timeout: installReplacementDialTimeout})
		if err == nil {
			_ = conn.Close()
			// A welcome proves a current image answered; it does not mean the
			// residual process counted by the install accepted a drain.
			welcomed = true
			continue
		}
		reason := codexbroker.RefusalOf(err)
		if reason != codexbroker.RefusalDrainRequired && reason != codexbroker.RefusalHostClosed && !target.gone() {
			fail(string(codexbroker.DialStageOf(err)), string(reason))
			continue
		}
		targets = append(targets, target)
		if result.failureStage == "" && (reason == codexbroker.RefusalDrainRequired || reason == codexbroker.RefusalHostClosed) {
			result.refusal = string(reason)
		}
	}
	result.accepted = len(targets)
	if result.accepted < len(residual) && result.failureStage == "" {
		if welcomed {
			fail(string(codexbroker.DialStageHandshake), "")
		} else {
			fail(string(codexbroker.DialStageDiscovery), string(codexbroker.RefusalHostUnavailable))
		}
	}
	result.drained = func() int {
		count := 0
		for _, target := range targets {
			if target.gone() {
				count++
			}
		}
		return count
	}
	return result
}

// Socket identities stay in memory. A path alone would confuse an old runtime
// with its successor, and a missing unrelated/default path proves nothing.
type installReplacementSocket struct {
	path      string
	info      os.FileInfo
	discovery codexbroker.Discovery
	runtime   string
}

func (target installReplacementSocket) gone() bool {
	latest, err := os.Lstat(target.path)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	return err == nil && target.superseded(latest)
}

// An unlinked socket's inode can be immediately reused by its successor on
// ext4. SameFile alone would then wait for the new runtime to exit. Compare the
// ownership-checked publication as well, without dialing that new runtime.
// Missing, malformed, or untrusted records do not prove a successor exists.
func (target installReplacementSocket) superseded(latest os.FileInfo) bool {
	if !os.SameFile(target.info, latest) {
		return true
	}
	runtimeID, err := codexbroker.PublishedRuntimeID(target.discovery)
	return err == nil && target.runtime != "" && runtimeID != target.runtime
}

// This bounded selection hint never supplies credentials or runtime authority.
func installReplacementRecordPID(path string) int {
	file, err := os.Open(path) // #nosec G304 -- path is a published record under this state domain.
	if err != nil {
		return 0
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, codexBrokerRecordLimit+1))
	if err != nil || len(payload) > codexBrokerRecordLimit {
		return 0
	}
	var record struct {
		PID int `json:"pid"`
	}
	if json.Unmarshal(payload, &record) != nil {
		return 0
	}
	return record.PID
}
