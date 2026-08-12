package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/lifecycle"
	corepreview "github.com/crevissepartners/projmux/internal/core/preview"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

const defaultSessionStatePruneAge = 30 * 24 * time.Hour

type pruneInventoryResolver interface {
	ListEphemeralSessions(ctx context.Context) ([]lifecycle.SessionInventory, error)
}

type pruneLiveSessionResolver interface {
	ExistingSessions(ctx context.Context) (map[string]bool, error)
}

type pruneSessionKiller interface {
	KillSession(ctx context.Context, sessionName string) error
}

type pruneCommand struct {
	diagnostics          *diagnostics.LifecycleRecorder
	inventory            pruneInventoryResolver
	liveSessions         pruneLiveSessionResolver
	killer               pruneSessionKiller
	reconcileNotify      func()
	cleanupKilledSession func(string)
	sessionStore         func() (sessionstate.Store, error)
	now                  func() time.Time
}

type previewSelectionDeleter interface {
	Delete(sessionName string) error
}

type killedSessionPreviewCleaner struct {
	store previewSelectionDeleter
	err   error
}

func newKilledSessionPreviewCleaner() *killedSessionPreviewCleaner {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return &killedSessionPreviewCleaner{err: err}
	}
	store := corepreview.NewDefaultStore(paths)
	return &killedSessionPreviewCleaner{store: store}
}

func (c *killedSessionPreviewCleaner) cleanup(sessionName string) {
	if c == nil || c.err != nil || c.store == nil {
		return
	}
	_ = c.store.Delete(sessionName)
}

func newPruneCommand(recorders ...*diagnostics.LifecycleRecorder) *pruneCommand {
	opts := []inttmux.ClientOption{}
	if len(recorders) > 0 && recorders[0] != nil {
		opts = append(opts, inttmux.WithLifecycleDiagnostics(recorders[0]))
	}
	client := inttmux.NewClient(inttmux.ExecRunner{}, opts...)
	return &pruneCommand{
		diagnostics:  recorderFrom(recorders),
		inventory:    client,
		liveSessions: client,
		killer:       client,
		sessionStore: sessionstate.NewDefaultStoreFromEnv,
		now:          time.Now,
	}
}

func (c *pruneCommand) Run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("prune", flag.ContinueOnError)
	fs.SetOutput(stderr)

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		printPruneUsage(stderr)
		return errors.New("prune requires a subcommand")
	}

	switch fs.Arg(0) {
	case "ephemeral":
		return c.runEphemeral(fs.Args()[1:], stdout, stderr)
	case "session-state":
		return c.runSessionState(fs.Args()[1:], stdout, stderr)
	case "help", "--help", "-h":
		printPruneUsage(stdout)
		return nil
	default:
		printPruneUsage(stderr)
		return fmt.Errorf("unknown prune subcommand: %s", fs.Arg(0))
	}
}

func (c *pruneCommand) runEphemeral(args []string, _ io.Writer, stderr io.Writer) error {
	fs := flag.NewFlagSet("prune ephemeral", flag.ContinueOnError)
	fs.SetOutput(stderr)
	keepCount := fs.Int("keep", 3, "number of unattached ephemeral sessions to retain")

	if err := fs.Parse(args); err != nil {
		printPruneUsage(stderr)
		return err
	}
	if fs.NArg() != 0 {
		printPruneUsage(stderr)
		return fmt.Errorf("prune ephemeral does not accept positional arguments")
	}
	if c.inventory == nil {
		return fmt.Errorf("resolve ephemeral sessions to prune: inventory resolver is not configured")
	}

	sessions, err := c.inventory.ListEphemeralSessions(context.Background())
	if err != nil {
		return fmt.Errorf("resolve ephemeral sessions to prune: %w", err)
	}

	targets, err := lifecycle.PruneEphemeralTargets(sessions, *keepCount)
	if err != nil {
		return fmt.Errorf("plan ephemeral prune: %w", err)
	}
	if len(targets) == 0 {
		return nil
	}
	if c.killer == nil {
		return fmt.Errorf("kill ephemeral sessions to prune: killer is not configured")
	}

	killedAny := false
	defer func() {
		if killedAny && c.reconcileNotify != nil {
			c.reconcileNotify()
		}
	}()
	for _, target := range targets {
		if c.diagnostics != nil {
			c.diagnostics.Mark(diagnostics.OperationSessionKill)
		}
		if err := c.killer.KillSession(context.Background(), target); err != nil {
			return fmt.Errorf("kill ephemeral session %q: %w", target, err)
		}
		killedAny = true
		if c.cleanupKilledSession != nil {
			c.cleanupKilledSession(target)
		}
	}

	return nil
}

func (c *pruneCommand) runSessionState(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "delete" {
		return c.runSessionStateDelete(args[1:], stdout, stderr)
	}

	fs := flag.NewFlagSet("prune session-state", flag.ContinueOnError)
	fs.SetOutput(stderr)
	olderThan := fs.Duration("older-than", defaultSessionStatePruneAge, "age after which a live-session snapshot is listed")
	if err := fs.Parse(args); err != nil {
		printPruneUsage(stderr)
		return err
	}
	if fs.NArg() != 0 {
		printPruneUsage(stderr)
		return fmt.Errorf("prune session-state does not accept positional arguments")
	}
	if *olderThan < 0 {
		printPruneUsage(stderr)
		return fmt.Errorf("prune session-state --older-than must be non-negative")
	}
	if c.liveSessions == nil {
		return fmt.Errorf("resolve live sessions for snapshot prune: session resolver is not configured")
	}
	liveSessions, err := c.liveSessions.ExistingSessions(context.Background())
	if err != nil {
		return fmt.Errorf("resolve live sessions for snapshot prune: %w", err)
	}
	store, err := c.resolveSessionStore()
	if err != nil {
		return err
	}
	now := time.Now()
	if c.now != nil {
		now = c.now()
	}
	candidates, err := sessionStatePruneCandidates(store, liveSessions, now, *olderThan, stderr)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		_, err := fmt.Fprintln(stdout, "no stale session snapshots")
		return err
	}
	for _, candidate := range candidates {
		if _, err := fmt.Fprintf(stdout, "%s\t%s\t%s\n",
			candidate.Session,
			strings.Join(candidate.Reasons, ","),
			candidate.SavedAt.UTC().Format(time.RFC3339),
		); err != nil {
			return err
		}
	}
	return nil
}

func (c *pruneCommand) runSessionStateDelete(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printPruneUsage(stderr)
		return fmt.Errorf("prune session-state delete requires at least 1 session")
	}
	store, err := c.resolveSessionStore()
	if err != nil {
		return err
	}

	seen := make(map[string]struct{}, len(args))
	for _, arg := range args {
		sessionName := strings.TrimSpace(arg)
		if sessionName == "" {
			printPruneUsage(stderr)
			return fmt.Errorf("prune session-state delete requires non-empty session names")
		}
		if _, ok := seen[sessionName]; ok {
			continue
		}
		seen[sessionName] = struct{}{}
		if err := store.Delete(sessionName); err != nil {
			return fmt.Errorf("delete session snapshot %q: %w", sessionName, err)
		}
		if _, err := fmt.Fprintf(stdout, "deleted session snapshot: %s\n", sessionName); err != nil {
			return err
		}
	}
	return nil
}

func (c *pruneCommand) resolveSessionStore() (sessionstate.Store, error) {
	if c.sessionStore == nil {
		return sessionstate.Store{}, fmt.Errorf("configure snapshot prune: session store is not configured")
	}
	store, err := c.sessionStore()
	if err != nil {
		return sessionstate.Store{}, fmt.Errorf("configure snapshot prune: %w", err)
	}
	return store, nil
}

type sessionStatePruneCandidate struct {
	Session string
	SavedAt time.Time
	Reasons []string
}

func sessionStatePruneCandidates(
	store sessionstate.Store,
	liveSessions map[string]bool,
	now time.Time,
	olderThan time.Duration,
	stderr io.Writer,
) ([]sessionStatePruneCandidate, error) {
	entries, err := os.ReadDir(store.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list session snapshots: %w", err)
	}

	cutoff := now.Add(-olderThan)
	candidates := make([]sessionStatePruneCandidate, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		sessionName := strings.TrimSuffix(entry.Name(), ".json")
		summary, err := store.Summary(sessionName)
		if err != nil {
			if stderr != nil {
				_, _ = fmt.Fprintf(stderr, "warning: inspect session snapshot %q: %v\n", sessionName, err)
			}
			continue
		}
		var reasons []string
		if !liveSessions[summary.Session] {
			reasons = append(reasons, "dead")
		}
		if !summary.SavedAt.After(cutoff) {
			reasons = append(reasons, "old")
		}
		if len(reasons) == 0 {
			continue
		}
		candidates = append(candidates, sessionStatePruneCandidate{
			Session: summary.Session,
			SavedAt: summary.SavedAt,
			Reasons: reasons,
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].SavedAt.Equal(candidates[j].SavedAt) {
			return candidates[i].Session < candidates[j].Session
		}
		return candidates[i].SavedAt.Before(candidates[j].SavedAt)
	})
	return candidates, nil
}

func printPruneUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux prune ephemeral [--keep=N]")
	fmt.Fprintln(w, "  projmux prune session-state [--older-than=720h]")
	fmt.Fprintln(w, "  projmux prune session-state delete <session>...")
}
