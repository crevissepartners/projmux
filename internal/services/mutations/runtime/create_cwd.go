package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (c *CreateExecution) SplitPaneLaunchDir(ctx context.Context, anchorPaneID, root string) (string, string) {
	live, err := c.Runtime.ReadTmux(ctx, "display-message", "-p", "-t", anchorPaneID, "-F", "#{pane_current_path}")
	return ResolveSplitPaneCWD(root, live, err)
}
func ResolveSplitPaneCWD(root, live string, readErr error) (string, string) {
	root = strings.TrimSpace(root)
	if readErr != nil {
		return root, splitCWDUnreadableNotice(root, readErr)
	}
	live = strings.TrimSpace(live)
	if live == "" {
		return root, splitCWDUnreadableNotice(root, errors.New("tmux reported no directory"))
	}
	canonicalRoot, err := CanonicalExistingDir(root)
	if err != nil {
		return root, splitCWDUnreadableNotice(root, fmt.Errorf("the Project root %w", err))
	}
	canonicalLive, err := CanonicalExistingDir(live)
	if err != nil {
		return root, splitCWDUnreadableNotice(root, fmt.Errorf("%q %w", live, err))
	}
	if canonicalLive == canonicalRoot {
		return root, ""
	}
	if PathWithinTree(canonicalRoot, canonicalLive) {
		return canonicalLive, ""
	}
	return root, fmt.Sprintf("split started in Project root %q: active Pane directory %q is outside it", root, live)
}

func splitCWDUnreadableNotice(root string, err error) string {
	return fmt.Sprintf("split started in Project root %q: active Pane directory could not be read: %s", root, strings.Join(strings.Fields(err.Error()), " "))
}
func CanonicalExistingDir(raw string) (string, error) {
	if !filepath.IsAbs(strings.TrimSpace(raw)) {
		return "", ErrWorkspaceNotAbsolute
	}
	clean := filepath.Clean(strings.TrimSpace(raw))
	info, err := os.Stat(clean)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("is not a directory")
	}
	real, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", err
	}
	return filepath.Clean(real), nil
}
func PathWithinTree(anchor, path string) bool {
	anchor = strings.TrimSpace(anchor)
	path = strings.TrimSpace(path)
	if anchor == "" || path == "" {
		return false
	}
	rel, err := filepath.Rel(anchor, path)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, "../"))
}

var ErrWorkspaceNotAbsolute = errors.New("must be an absolute existing directory")
