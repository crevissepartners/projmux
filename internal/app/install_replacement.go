package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
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
	// requestDrain asks one live broker runtime to stand down and reports
	// whether a live runtime answered at all, plus the refusal that closed the
	// request. A nil reader makes the pass a census with no request, which is
	// what an unsupported platform gets.
	requestDrain func(ctx context.Context) (reached bool, refusal string)
	// runtimeGone reports that no broker runtime is published any more. It is
	// how the settle wait proves a drain finished without asking the runtime
	// to describe itself while it is closing.
	runtimeGone func() bool
	settle      time.Duration
	poll        time.Duration
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
		settle:      installReplacementSettle,
		poll:        installReplacementPoll,
	}
	command.requestDrain = defaultInstallReplacementDrainRequest
	command.runtimeGone = defaultInstallReplacementRuntimeGone
	return command
}

// runInstallReplacement is the route entrypoint.
//
// It always returns nil, for the reason the residue census always does: this
// runs as a step of an install that has already succeeded, and a step that can
// fail the thing it completes is worse than no step. Everything it could not do
// is on the record it writes and on the L2 row that reads it.
func runInstallReplacement(args []string, stderr io.Writer) error {
	if len(args) != 0 {
		return usageError("internal install-replace does not accept arguments")
	}
	newInstallReplacementCommand().Run(stderr)
	return nil
}

// Run takes the census, asks the drainable roles to stand down, waits a bounded
// moment, and records what happened.
func (c *installReplacementCommand) Run(stderr io.Writer) {
	if c == nil {
		return
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
}

// replace makes the one request this pass is allowed to make and watches for
// the answer.
func (c *installReplacementCommand) replace(outcome *installReplacementOutcome) {
	if outcome.Attempted == 0 {
		outcome.Outcome = installReplacementOutcomeNoTarget
		return
	}
	if c.requestDrain == nil {
		outcome.Outcome = installReplacementOutcomeUnreachable
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), installReplacementDialTimeout)
	defer cancel()
	reached, refusal := c.requestDrain(ctx)
	outcome.Refusal = refusal
	if !reached {
		// No live runtime answered the shipped path. The census counted a
		// residual broker process, so something is running an old image and
		// this pass cannot reach it: an unpublished runtime, a foreign-owned
		// artifact, a socket that outlived its host. Which one is on the
		// refusal, and none of them is a process this pass may end.
		outcome.Outcome = installReplacementOutcomeUnreachable
		return
	}
	if c.settleUntilGone() {
		outcome.Drained = outcome.Attempted
		outcome.Outcome = installReplacementOutcomeComplete
		return
	}
	outcome.Outcome = installReplacementOutcomePending
}

// settleUntilGone waits the bounded settle window for the runtime to close, and
// reports whether it did.
func (c *installReplacementCommand) settleUntilGone() bool {
	if c.runtimeGone == nil {
		return false
	}
	poll := c.poll
	if poll <= 0 {
		poll = installReplacementPoll
	}
	deadline := time.Now().Add(c.settle)
	for {
		if c.runtimeGone() {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(poll)
	}
}

// write places the outcome record. A failure is swallowed for the reason Run
// swallows everything: an unwritable state directory must not turn a completed
// install into a failed one.
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
			outcome.Drained, pluralizeInstallResidueProcesses(outcome.Drained))
	case installReplacementOutcomePending:
		return fmt.Sprintf(">> asked %d long-lived %s to stand down; %s still carrying work\n"+
			"   The runtime accepts no new work and goes when that work ends.\n",
			outcome.Attempted, pluralizeInstallResidueProcesses(outcome.Attempted),
			pluralizeInstallReplacementSubject(outcome.Attempted))
	case installReplacementOutcomeUnreachable:
		refusal := strings.TrimSpace(outcome.Refusal)
		if refusal == "" {
			refusal = "unknown"
		}
		return fmt.Sprintf(">> could not reach %d long-lived %s to replace: %s\n",
			outcome.Attempted, pluralizeInstallResidueProcesses(outcome.Attempted), refusal)
	default:
		return ""
	}
}

func pluralizeInstallReplacementSubject(count int) string {
	if count == 1 {
		return "it is"
	}
	return "they are"
}

// defaultInstallReplacementDrainRequest makes the drain request against the
// broker runtime published for this process's own state domain.
//
// It dials and never launches. `Dial` refuses when discovery finds no published
// runtime, which is exactly the answer this pass wants: a runtime that is not
// there needs no replacement, and one this pass started would be a residual
// process of its own making.
//
// A refused dial is not a failure here. The vintage trigger inside the runtime
// answers a compatible client with `drain-required`, so that refusal is the
// request being accepted, and it is reported as reached.
func defaultInstallReplacementDrainRequest(ctx context.Context) (bool, string) {
	discovery, err := defaultInstallReplacementDiscovery()
	if err != nil {
		return false, string(codexbroker.RefusalOf(err))
	}
	conn, err := codexbroker.Dial(ctx, discovery, codexbroker.DialConfig{Timeout: installReplacementDialTimeout})
	if err == nil {
		// A live runtime that welcomed this build is a runtime whose own image
		// is still the installed one. The census counted a residual broker
		// elsewhere, so this connection is not it; closing without a request
		// is the whole of what this pass may do about that.
		_ = conn.Close()
		return true, string(codexbroker.RefusalNone)
	}
	refusal := codexbroker.RefusalOf(err)
	if refusal == codexbroker.RefusalDrainRequired || refusal == codexbroker.RefusalHostClosed {
		return true, string(refusal)
	}
	return false, string(refusal)
}

// defaultInstallReplacementRuntimeGone reports that no runtime is published for
// this process's state domain any more.
//
// It is a discovery read rather than a dial, so it never re-enters the drain it
// is watching.
func defaultInstallReplacementRuntimeGone() bool {
	discovery, err := defaultInstallReplacementDiscovery()
	if err != nil {
		return false
	}
	info, err := os.Lstat(discovery.SocketPath())
	if err != nil {
		return true
	}
	return info.Mode()&os.ModeSocket == 0
}

func defaultInstallReplacementDiscovery() (codexbroker.Discovery, error) {
	domain, err := codexBrokerStateDomain(os.Getenv, os.UserHomeDir)
	if err != nil {
		return codexbroker.Discovery{}, err
	}
	return codexBrokerDiscoveryForEndpoint(domain, codexbroker.DefaultEndpointKey)
}
