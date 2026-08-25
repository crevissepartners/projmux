// Command anchorfixture prepares and inspects the final-v2 Agent-only Window
// shape used by the installed real-tmux materialization smoke. It is test-only:
// product routes never gain an offline graph-edit escape hatch.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		if _, writeErr := fmt.Fprintln(os.Stderr, err); writeErr != nil {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: anchorfixture rewrite <test-root> <source-relative> <destination-relative> <window-uid> <agent-uid> | inspect <test-root> <registry-relative> <window-uid> <agent-uid>")
	}
	switch args[0] {
	case "rewrite":
		if len(args) != 6 {
			return errors.New("rewrite requires test root, relative source, relative destination, window uid, and agent uid")
		}
		return withTestRoot(args[1], func(root *os.Root) error {
			return rewrite(root, args[2], args[3], args[4], args[5])
		})
	case "inspect":
		if len(args) != 5 {
			return errors.New("inspect requires test root, relative registry path, window uid, and agent uid")
		}
		return withTestRoot(args[1], func(root *os.Root) error {
			return inspect(root, args[2], args[3], args[4])
		})
	default:
		return fmt.Errorf("unknown mode %q", args[0])
	}
}

func withTestRoot(path string, fn func(*os.Root) error) (err error) {
	clean := filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(clean) || clean == string(filepath.Separator) {
		return fmt.Errorf("test root %q must be a non-root absolute path", path)
	}
	root, err := os.OpenRoot(clean)
	if err != nil {
		return fmt.Errorf("open test root: %w", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close test root: %w", closeErr))
		}
	}()
	return fn(root)
}

func load(root *os.Root, name string) (coremetadata.Registry, error) {
	raw, err := root.ReadFile(name)
	if err != nil {
		return coremetadata.Registry{}, err
	}
	var registry coremetadata.Registry
	if err := json.Unmarshal(raw, &registry); err != nil {
		return coremetadata.Registry{}, err
	}
	return registry, nil
}

func rewrite(root *os.Root, source, destination, windowUID, agentUID string) error {
	registry, err := load(root, source)
	if err != nil {
		return fmt.Errorf("load source Registry: %w", err)
	}
	window, ok := registry.Window(strings.TrimSpace(windowUID))
	if !ok {
		return fmt.Errorf("window %q does not exist", windowUID)
	}
	agent, ok := registry.Agent(strings.TrimSpace(agentUID))
	if !ok || agent.Metadata.OwnerUID() != window.Metadata.UID {
		return fmt.Errorf("agent %q is not owned by window %q", agentUID, windowUID)
	}
	anchor, ok := registry.Pane(agent.Status.PaneRef)
	if !ok || anchor.Spec.Role != coremetadata.PaneRoleAgent || anchor.Metadata.OwnerRef == nil ||
		anchor.Metadata.OwnerRef.Kind != coremetadata.KindAgent || anchor.Metadata.OwnerRef.UID != agent.Metadata.UID {
		return fmt.Errorf("agent %q has no exact managed Agent Pane", agentUID)
	}
	// DeleteFunc compacts the Pane slice in place, so retain the opaque identity
	// value rather than a pointer into the slice that is about to move.
	anchorUID := anchor.Metadata.UID
	removed := map[string]bool{}
	registry.Panes = slices.DeleteFunc(registry.Panes, func(pane coremetadata.Pane) bool {
		remove := pane.Spec.Role == coremetadata.PaneRoleShell && pane.Metadata.OwnerRef != nil &&
			pane.Metadata.OwnerRef.Kind == coremetadata.KindWindow && pane.Metadata.OwnerRef.UID == window.Metadata.UID
		if remove {
			removed[pane.Metadata.UID] = true
		}
		return remove
	})
	registry.NameReservations = slices.DeleteFunc(registry.NameReservations, func(reservation coremetadata.NameReservation) bool {
		return removed[reservation.UID]
	})
	window, _ = registry.Window(window.Metadata.UID)
	window.Spec.AnchorPaneRef = anchorUID
	window.Spec.DefaultShellPaneRef = ""
	if err := registry.Validate(); err != nil {
		return fmt.Errorf("validate Agent-only Registry: %w", err)
	}
	raw, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := writeAtomic(root, destination, raw); err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, anchorUID)
	return err
}

func writeAtomic(root *os.Root, name string, raw []byte) (err error) {
	tmpName := name + ".anchorfixture.tmp"
	if removeErr := root.Remove(tmpName); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
		return fmt.Errorf("remove stale fixture stage: %w", removeErr)
	}
	tmp, err := root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if removeErr := root.Remove(tmpName); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("remove fixture stage: %w", removeErr))
		}
	}()
	if _, err := tmp.Write(raw); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := tmp.Sync(); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return root.Rename(tmpName, name)
}

func inspect(root *os.Root, name, windowUID, agentUID string) error {
	registry, err := load(root, name)
	if err != nil {
		return err
	}
	if err := registry.Validate(); err != nil {
		return err
	}
	window, ok := registry.Window(strings.TrimSpace(windowUID))
	if !ok {
		return fmt.Errorf("window %q does not exist", windowUID)
	}
	anchor, ok := registry.WindowAnchor(window.Metadata.UID)
	if !ok {
		return fmt.Errorf("window %q has no valid anchor", windowUID)
	}
	defaultShell, ok := registry.WindowDefaultShell(window.Metadata.UID)
	if !ok {
		return fmt.Errorf("window %q has no valid default shell", windowUID)
	}
	agent, ok := registry.Agent(strings.TrimSpace(agentUID))
	if !ok {
		return fmt.Errorf("agent %q does not exist", agentUID)
	}
	_, err = fmt.Fprintf(os.Stdout, "%s\t%s\t%s\t%s\t%s\t%s\n",
		anchor.Metadata.UID,
		defaultShell.Metadata.UID,
		agent.Status.PaneRef,
		defaultShell.Metadata.OwnerRef.Kind,
		defaultShell.Metadata.OwnerRef.UID,
		defaultShell.Spec.Role,
	)
	return err
}
