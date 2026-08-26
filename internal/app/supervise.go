package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// These process-private variables bind provider hooks to the materialization
// that emitted them. They are intentionally not PROJMUX_* public hook API:
// providers merely inherit them from the internal supervisor and hook ingest
// consumes them as capability evidence.
const (
	internalActivationPaneUIDEnv    = "PMX_INTERNAL_ACTIVATION_PANE_UID"
	internalActivationGenerationEnv = "PMX_INTERNAL_ACTIVATION_GENERATION"
)

// superviseSpec is the identity one supervised launch quotes back when its
// child stops.
//
// Everything in it is minted by projmux before the pane exists. Nothing is read
// out of the pane, the shell, or the provider afterwards, which is what keeps
// the receipt an assertion about a process this build started rather than a
// guess about a process it found.
type superviseSpec struct {
	PaneUID     string
	AgentUID    string
	Generation  string
	OperationID string
	// RuntimeID is the exact raw tmux Pane handle observed by the activation
	// gate after tmux started this generation. It is intentionally absent from
	// the supervisor argv because no raw Pane exists when that argv is planned;
	// activation-exec fills it only after the committed Registry binding agrees
	// with its inherited TMUX_PANE.
	RuntimeID string
	// RegistryPath is the creator-resolved private authority for Agent
	// admission. It is carried only between internal routes and is never
	// exported to providers or public PROJMUX_* hooks.
	RegistryPath string
}

// valid reports whether the spec can identify a receipt at all.
func (s superviseSpec) valid() bool {
	return strings.TrimSpace(s.PaneUID) != "" && strings.TrimSpace(s.Generation) != ""
}

func exactActivationRegistryPath(path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return errors.New("agent activation requires an absolute Registry path")
	}
	if filepath.Clean(path) != path {
		return errors.New("agent activation requires a clean Registry path")
	}
	stateDir := filepath.Dir(filepath.Dir(path))
	if intmetadata.PathFor(stateDir) != path {
		return errors.New("agent activation Registry path has an unexpected shape")
	}
	return nil
}

// superviseCommand implements the hidden `internal supervise` route: the
// managed process supervisor that every managed shell and Agent pane execs.
//
// It is deliberately not a public verb. Its argv is constructed by the
// materializer that also minted the generation, so an operator typing it by
// hand could only produce a receipt for a generation they do not own -- which
// the registry guard would refuse anyway.
type superviseCommand struct {
	store *resourceStore
	// store is a poison seam used to prove that post-exit receipt recording
	// never enters the Registry. Pre-launch ownership admission belongs to the
	// activation-exec child, while journal remains the supervisor's lock-free
	// transport after that child exits.
	journal terminationJournal
	// run executes the child and returns its reaped outcome. Production wires
	// the platform implementation; tests replace it.
	run func(argv []string, argv0 string) (processOutcome, error)
	// runActivation is the production runner. The legacy-shaped run seam stays
	// for focused tests; when present, this one exports exact generation
	// evidence only to the supervised provider process and its hook children.
	runActivation func(argv []string, argv0 string, spec superviseSpec) (processOutcome, error)
	now           func() time.Time
	warn          io.Writer
}

func newSuperviseCommand() *superviseCommand {
	return &superviseCommand{
		store: newResourceStore(),
		run:   runSupervisedChild, runActivation: runSupervisedChildWithActivation,
		now: time.Now,
	}
}

// processOutcome is one reaped child's wait status.
type processOutcome struct {
	// ExitCode is the status the child exited with. It is only meaningful when
	// Signal is empty.
	ExitCode int
	// Signal is the name of the signal that killed the child, without the
	// "SIG" prefix, or "" for a child that exited on its own.
	Signal string
	// SignalNumber is the platform number behind Signal. It is carried rather
	// than re-derived from the name so the exit status the supervisor reports
	// comes from the same wait status the receipt does.
	SignalNumber int
}

// exitStatus renders the outcome as the status the supervisor exits with.
// A signalled child reports 128+signum, which is the encoding every POSIX
// shell already uses for exactly this.
func (o processOutcome) exitStatus() int {
	if o.Signal != "" {
		return 128 + o.SignalNumber
	}
	return o.ExitCode
}

// superviseExitError propagates the child's status as the supervisor's own
// without printing anything: the child already owned this pane's output.
type superviseExitError struct{ code int }

func (e superviseExitError) Error() string {
	return fmt.Sprintf("supervised process exited with status %d", e.code)
}
func (e superviseExitError) ExitCode() int { return e.code }

// Run supervises one child process and records its actual exit evidence.
func (c *superviseCommand) Run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("internal supervise", flag.ContinueOnError)
	fs.SetOutput(stderr)
	paneUID := fs.String("pane-uid", "", "Pane resource uid this process is the runtime of")
	agentUID := fs.String("agent-uid", "", "Agent resource uid owning the Pane, for an Agent-managed launch")
	generation := fs.String("generation", "", "activation generation this launch was issued")
	operationID := fs.String("operation-id", "", "create/resume operation that issued the generation")
	registryPath := fs.String("registry-path", "", "private creator-resolved Registry authority for an Agent launch")
	argv0 := fs.String("argv0", "", "argv[0] the child is exec'd with; a leading '-' requests a login shell")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError(err.Error())
	}
	child := fs.Args()
	if len(child) == 0 {
		return usageError("internal supervise requires a command after --")
	}
	spec := superviseSpec{
		PaneUID:      strings.TrimSpace(*paneUID),
		AgentUID:     strings.TrimSpace(*agentUID),
		Generation:   strings.TrimSpace(*generation),
		OperationID:  strings.TrimSpace(*operationID),
		RegistryPath: *registryPath,
	}
	if !spec.valid() {
		return usageError("internal supervise requires --pane-uid and --generation")
	}
	if c.run == nil && c.runActivation == nil {
		return errors.New("internal supervise: no process supervisor is configured")
	}

	var (
		outcome processOutcome
		err     error
	)
	if c.runActivation != nil {
		outcome, err = c.runActivation(child, strings.TrimSpace(*argv0), spec)
	} else {
		outcome, err = c.run(child, strings.TrimSpace(*argv0))
	}
	if err != nil {
		// The child never started. That is a launch failure, not a
		// termination: no process of this generation ever ran, so there is no
		// exit to report and inventing one would be evidence fabrication.
		return fmt.Errorf("internal supervise: start %s: %w", child[0], err)
	}

	c.recordOutcome(spec, outcome, stderr)
	return superviseExitError{code: outcome.exitStatus()}
}

// recordOutcome appends the observed receipt, best effort.
//
// A failure here is deliberately not propagated. The supervisor's contract to
// the operator is that the pane behaves exactly as it did before supervision
// existed; a registry that is locked, read-only, or gone must not change the
// status the pane reports. This prewrite never opens the Registry or waits on
// its lock; the next controller convergence absorbs it under the ordinary
// Registry transaction before it projects the observed absence.
func (c *superviseCommand) recordOutcome(spec superviseSpec, outcome processOutcome, stderr io.Writer) {
	receipt := coremetadata.TerminationEvidence{
		Source:         coremetadata.TerminationSourceSupervisor,
		Classification: coremetadata.ClassifyProcessExit(outcome.ExitCode, outcome.Signal),
		ObservedAt:     c.clock()().UTC(),
		PaneUID:        spec.PaneUID,
		AgentUID:       spec.AgentUID,
		Generation:     spec.Generation,
		OperationID:    spec.OperationID,
	}
	if outcome.Signal != "" {
		receipt.Signal = outcome.Signal
	} else {
		code := outcome.ExitCode
		receipt.ExitCode = &code
	}
	journal := c.journal
	if strings.TrimSpace(journal.path) == "" {
		var err error
		if spec.AgentUID != "" {
			journal, err = terminationJournalForRegistryPath(spec.RegistryPath)
		} else {
			journal, err = newTerminationJournal(nil, nil)
		}
		if err != nil {
			c.diagnose(stderr, "resolve termination journal: %v", err)
			return
		}
	}
	if err := journal.append(receipt); err != nil {
		c.diagnose(stderr, "append termination receipt: %v", err)
	}
}

func (c *superviseCommand) diagnose(stderr io.Writer, format string, args ...any) {
	sink := c.warn
	if sink == nil {
		sink = stderr
	}
	if sink == nil {
		return
	}
	fmt.Fprintf(sink, "projmux: internal supervise: "+format+"\n", args...)
}

func (c *superviseCommand) clock() func() time.Time {
	if c.now != nil {
		return c.now
	}
	return time.Now
}
