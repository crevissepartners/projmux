package app

import (
	"fmt"
	"io"
	"strings"
)

func (c *previewCommand) runSelect(args []string, stdout, stderr io.Writer) error {
	sessionName, windowIndex, paneIndex, err := parsePreviewSelectArgs(args, stderr)
	if err != nil {
		return err
	}

	store, err := c.requireStore()
	if err != nil {
		return err
	}

	if err := store.WriteSelection(sessionName, windowIndex, paneIndex); err != nil {
		return fmt.Errorf("persist preview selection for %q: %w", sessionName, err)
	}

	return nil
}

func parsePreviewSelectArgs(args []string, stderr io.Writer) (string, string, string, error) {
	if len(args) != 2 && len(args) != 3 {
		printPreviewUsage(stderr)
		return "", "", "", fmt.Errorf("preview select requires 2 or 3 arguments: <session> <window> [pane]")
	}

	sessionName := strings.TrimSpace(args[0])
	if sessionName == "" {
		printPreviewUsage(stderr)
		return "", "", "", fmt.Errorf("preview select requires a non-empty <session> argument")
	}

	windowIndex := strings.TrimSpace(args[1])
	if windowIndex == "" {
		printPreviewUsage(stderr)
		return "", "", "", fmt.Errorf("preview select requires a non-empty <window> argument")
	}

	paneIndex := ""
	if len(args) == 3 {
		paneIndex = strings.TrimSpace(args[2])
		if paneIndex == "" {
			printPreviewUsage(stderr)
			return "", "", "", fmt.Errorf("preview select requires a non-empty <pane> argument when provided")
		}
	}

	return sessionName, windowIndex, paneIndex, nil
}
