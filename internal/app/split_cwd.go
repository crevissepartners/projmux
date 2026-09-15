package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
)

// splitCWDSource names where a new split's shell or Agent starts. It is launch
// data only: it never selects the owner Project, Window, anchor, or session.
type splitCWDSource string

const (
	// splitCWDFromProject starts every split in the owner Project root. It is
	// the default, and it issues no tmux command of its own.
	splitCWDFromProject splitCWDSource = "project"
	// splitCWDFromPane starts a split in the active Pane's live directory while
	// that directory is inside the owner Project root.
	splitCWDFromPane splitCWDSource = "pane"

	// splitCWDFromFlag is the per-call spelling shared by every split create.
	splitCWDFromFlag = "cwd-from"
)

// parseSplitCWDSource accepts exactly the two closed source names.
func parseSplitCWDSource(raw string) (splitCWDSource, bool) {
	switch source := splitCWDSource(strings.TrimSpace(raw)); source {
	case splitCWDFromProject, splitCWDFromPane:
		return source, true
	default:
		return "", false
	}
}

// resolveSplitCWDSource picks the split start source: an explicit --cwd-from,
// then `[ai] split_cwd_from` in the owner Project root's `.projmux/config.toml`,
// then the global config, then project. The project tier is always read from
// the owner Project root, never from a Pane directory, so where a Pane sits
// cannot change which config decides. A tier whose value is missing, unknown,
// or unreadable is skipped. There is no environment tier.
func resolveSplitCWDSource(flagValue, projectRoot string, homeDir func() (string, error), lookupEnv func(string) string) splitCWDSource {
	if source, ok := parseSplitCWDSource(flagValue); ok {
		return source
	}
	if root := strings.TrimSpace(projectRoot); root != "" {
		if cfg, err := hooks.LoadProjectConfigFile(filepath.Join(root, ".projmux", "config.toml")); err == nil {
			if source, ok := parseSplitCWDSource(cfg.AI.SplitCWDFrom); ok {
				return source
			}
		}
	}
	if homeDir != nil {
		getenv := lookupEnv
		if getenv == nil {
			getenv = func(string) string { return "" }
		}
		if path, err := hooks.GlobalConfigPath(getenv, homeDir); err == nil {
			if cfg, err := hooks.LoadGlobalConfig(path); err == nil {
				if source, ok := parseSplitCWDSource(cfg.AI.SplitCWDFrom); ok {
					return source
				}
			}
		}
	}
	return splitCWDFromProject
}

// splitCWDSource resolves the source with this command's config seams.
func (c *createCommand) splitCWDSource(flagValue, projectRoot string) splitCWDSource {
	return resolveSplitCWDSource(flagValue, projectRoot, c.homeDir, c.lookupEnv)
}

// splitPaneLaunchDir reads one exact anchor Pane's live directory once and
// applies the owner Project root rule to it.
func (c *createCommand) splitPaneLaunchDir(ctx context.Context, anchorPaneID, root string) (string, string) {
	live, err := c.runtime.read(ctx, "display-message", "-p", "-t", anchorPaneID, "-F", "#{pane_current_path}")
	return resolveSplitPaneCWD(root, live, err)
}

// resolveSplitPaneCWD is the owner Project root rule. It returns the directory
// the split starts in and, when that is the root because the Pane directory was
// not usable, a one-line notice naming the reason and the root.
//
// The live directory and the root are both canonicalized before the tree
// comparison, so a symlinked root or cwd is judged by where it really points.
// The Pane directory is kept only when it is a descendant of the root. Anything
// else -- $HOME, another registered Project tree, a missing directory, a failed
// read -- starts in the root and is never refused. A Pane sitting at the root
// itself returns the stored root spelling, so it launches exactly like the
// project source.
func resolveSplitPaneCWD(root, live string, readErr error) (string, string) {
	root = strings.TrimSpace(root)
	if readErr != nil {
		return root, splitCWDUnreadableNotice(root, readErr)
	}
	live = strings.TrimSpace(live)
	if live == "" {
		return root, splitCWDUnreadableNotice(root, errors.New("tmux reported no directory"))
	}
	canonicalRoot, err := canonicalExistingDir(root)
	if err != nil {
		return root, splitCWDUnreadableNotice(root, fmt.Errorf("the Project root %w", err))
	}
	canonicalLive, err := canonicalExistingDir(live)
	if err != nil {
		return root, splitCWDUnreadableNotice(root, fmt.Errorf("%q %w", live, err))
	}
	if canonicalLive == canonicalRoot {
		return root, ""
	}
	if pathWithinTree(canonicalRoot, canonicalLive) {
		return canonicalLive, ""
	}
	return root, fmt.Sprintf("split started in Project root %q: active Pane directory %q is outside it", root, live)
}

func splitCWDUnreadableNotice(root string, err error) string {
	return fmt.Sprintf("split started in Project root %q: active Pane directory could not be read: %s",
		root, strings.Join(strings.Fields(err.Error()), " "))
}

// splitCWDNoticeLine is the one stderr line a CLI split create prints per
// anchor whose Pane directory fell back to the root.
func splitCWDNoticeLine(spelling, windowName, notice string) string {
	return fmt.Sprintf("%s: window/%s: %s", spelling, windowName, notice)
}

// writeSplitCWDNotices prints the committed create's fallback notices. It runs
// only after the transaction committed, so a refused create prints none.
func writeSplitCWDNotices(stderr io.Writer, notices []string) error {
	for _, notice := range notices {
		if _, err := fmt.Fprintln(stderr, notice); err != nil {
			return err
		}
	}
	return nil
}

// intentSplitLaunchDir resolves where a canonical UI split starts, after the
// exact origin scope is resolved and before the Registry transaction opens. The
// active Pane is the intent's origin Pane: the Pane the key was pressed in, or
// the Pane a menu was opened on.
//
// It returns "" -- the unchanged launch -- for a ControlSession root, a resumed
// conversation, the project source, and a Pane sitting at the root, so only a
// usable Pane directory inside the root changes anything. For a Project root
// scope.cwd is that root; it is read here as the config location and the
// fallback and is never written back, because it also names the session.
func (c *createCommand) intentSplitLaunchDir(scope canonicalIntentScope, conversation string) (string, string) {
	if scope.rootKind != coremetadata.KindProject || strings.TrimSpace(conversation) != "" {
		return "", ""
	}
	root := scope.cwd
	if c.splitCWDSource("", root) != splitCWDFromPane {
		return "", ""
	}
	dir, notice := c.splitPaneLaunchDir(context.Background(), scope.anchorPaneID, root)
	if dir == root {
		dir = ""
	}
	return dir, notice
}

// finishSplitIntent reports a committed UI split's fallback notice as one
// stderr line for the UI surface that owns the client to show. A refused create
// reports only its refusal.
func finishSplitIntent(stderr io.Writer, notice string, err error) error {
	if err != nil {
		return visibleCanonicalCreateError(err)
	}
	if notice != "" && stderr != nil {
		_, _ = fmt.Fprintln(stderr, notice)
	}
	return nil
}
