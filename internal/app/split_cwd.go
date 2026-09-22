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

// splitCWDOrigin names which tier decided a UI split's start source. It is a
// display concern: only the UI surfaces (the Settings row) report it, and the
// CLI has no tier to report.
type splitCWDOrigin string

const (
	// splitCWDOriginFlag is an explicit per-call value.
	splitCWDOriginFlag splitCWDOrigin = "flag"
	// splitCWDOriginProject is `[ai] split_cwd_from` in the owner Project
	// root's `.projmux/config.toml`.
	splitCWDOriginProject splitCWDOrigin = "project"
	// splitCWDOriginGlobal is `[ai] split_cwd_from` in the global config.
	splitCWDOriginGlobal splitCWDOrigin = "global"
	// splitCWDOriginDefault is the built-in fallback, `project`.
	splitCWDOriginDefault splitCWDOrigin = "default"
)

// splitCWDResolution is a resolved UI split start source plus the tier that
// decided it.
type splitCWDResolution struct {
	Source splitCWDSource
	Origin splitCWDOrigin
}

// splitCWDConfigReaders is the pair of config seams the UI tier chain reads
// through. It is a package variable so a test can count the reads on both
// sides of the CLI/UI boundary: the whole point of that boundary is that the
// CLI path reaches neither reader, and an absence that is only asserted from
// the outcome would also pass if the file merely failed to parse.
type splitCWDConfigReaders struct {
	project func(path string) (hooks.ProjectConfig, error)
	global  func(path string) (hooks.ProjectConfig, error)
}

var splitCWDConfigSeam = splitCWDConfigReaders{
	project: hooks.LoadProjectConfigFile,
	global:  hooks.LoadGlobalConfig,
}

// cliSplitCWDSource resolves where a CLI split starts: `--cwd-from` alone.
//
// A CLI result is determined by its arguments. `create pane`, `create agent`
// and the provider shortcuts therefore open no config file at all on this
// route, and with no flag every CLI split starts in the owner Project root.
// Scripts and automation must not change behavior because a human changed a
// Settings value, so the `[ai] split_cwd_from` tiers below are deliberately
// out of reach here. `--cwd-from pane` still obeys the owner Project root rule
// and still prints the fallback notice.
func cliSplitCWDSource(flagValue string) splitCWDSource {
	if source, ok := parseSplitCWDSource(flagValue); ok {
		return source
	}
	return splitCWDFromProject
}

// resolveUISplitCWDSource picks a UI split's start source: an explicit
// per-call value, then `[ai] split_cwd_from` in the owner Project root's
// `.projmux/config.toml`, then the global config, then project. The project
// tier is always read from the owner Project root, never from a Pane
// directory, so where a Pane sits cannot change which config decides. A tier
// whose value is missing, unknown, or unreadable is skipped. There is no
// environment tier.
//
// This tiered chain belongs to the UI intents -- the keybinding split, the
// Alt-7 launcher, the resume picker `new` row and the Pane right-click menu --
// and to the Settings row that shows the same resolved value. The CLI uses
// cliSplitCWDSource instead and never consults it.
func resolveUISplitCWDSource(flagValue, projectRoot string, homeDir func() (string, error), lookupEnv func(string) string) splitCWDResolution {
	if source, ok := parseSplitCWDSource(flagValue); ok {
		return splitCWDResolution{Source: source, Origin: splitCWDOriginFlag}
	}
	if root := strings.TrimSpace(projectRoot); root != "" {
		if cfg, err := splitCWDConfigSeam.project(filepath.Join(root, ".projmux", "config.toml")); err == nil {
			if source, ok := parseSplitCWDSource(cfg.AI.SplitCWDFrom); ok {
				return splitCWDResolution{Source: source, Origin: splitCWDOriginProject}
			}
		}
	}
	if homeDir != nil {
		getenv := lookupEnv
		if getenv == nil {
			getenv = func(string) string { return "" }
		}
		if path, err := hooks.GlobalConfigPath(getenv, homeDir); err == nil {
			if cfg, err := splitCWDConfigSeam.global(path); err == nil {
				if source, ok := parseSplitCWDSource(cfg.AI.SplitCWDFrom); ok {
					return splitCWDResolution{Source: source, Origin: splitCWDOriginGlobal}
				}
			}
		}
	}
	return splitCWDResolution{Source: splitCWDFromProject, Origin: splitCWDOriginDefault}
}

// uiSplitCWDSource resolves the UI split start source with this command's
// config seams.
func (c *createCommand) uiSplitCWDSource(flagValue, projectRoot string) splitCWDResolution {
	return resolveUISplitCWDSource(flagValue, projectRoot, c.homeDir, c.lookupEnv)
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
// This is the one split route that reads the `[ai] split_cwd_from` tiers, via
// resolveUISplitCWDSource. Every UI surface that starts a split funnels through
// here, which is what makes the Settings row mean something; the CLI routes
// resolve from `--cwd-from` alone.
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
	if c.uiSplitCWDSource("", root).Source != splitCWDFromPane {
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
