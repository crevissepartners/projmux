package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/crevissepartners/projmux/internal/app/hookcmd"
	"github.com/crevissepartners/projmux/internal/app/keybinding"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

// newHookCommand builds `projmux hook` over homeDir, lookupEnv, and getwd,
// resolving config paths, locale, the missing-HOME reason, and operands the
// way every other route does.
func newHookCommand(homeDir func() (string, error), lookupEnv func(string) string, getwd func() (string, error), stdin io.Reader, editorRunner hookcmd.EditorRunner) *hookcmd.Command {
	return hookcmd.New(homeDir, lookupEnv, getwd, stdin, editorRunner, hookcmd.Deps{
		ConfigPaths:     configPaths,
		Locale:          appLocale,
		LocalizeText:    LocalizeText,
		IsMissingHome:   isMissingHome,
		PathOrReason:    pathOrReason,
		SplitOperands:   splitOperands,
		PrintRouteNotes: printRouteNotes,
	})
}

const (
	hookTrustPopupTitle           = "Trust project automation"
	hookTrustPopupWidth           = "90"
	hookTrustPopupHeight          = "24"
	hookTrustInlineEnv            = "PROJMUX_HOOK_TRUST_INLINE"
	hookTrustPopupTargetClientEnv = "PROJMUX_HOOK_TRUST_TARGET_CLIENT"
	hookTrustPopupTargetPaneEnv   = "PROJMUX_HOOK_TRUST_TARGET_PANE"
)

func tmuxProjectHookPrompt(lookupEnv func(string) string, executable func() (string, error), runner tmuxRunner) hooks.ProjectHookPrompt {
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	return func(req hooks.ProjectHookPromptRequest) hooks.ProjectHookDecision {
		if strings.TrimSpace(lookupEnv("TMUX")) == "" || executable == nil || runner == nil {
			return hooks.ProjectHookDeny
		}
		binaryPath, err := executable()
		if err != nil || strings.TrimSpace(binaryPath) == "" {
			return hooks.ProjectHookDeny
		}
		decision, err := runTmuxHookTrustPopup(context.Background(), runner, binaryPath, req, hookTrustPopupTarget{
			client: firstNonEmpty(
				lookupEnv(hookTrustPopupTargetClientEnv),
				lookupEnv("PROJMUX_POPUP_TARGET_CLIENT"),
			),
			pane: firstNonEmpty(
				lookupEnv(hookTrustPopupTargetPaneEnv),
				lookupEnv("PROJMUX_POPUP_TARGET_PANE"),
			),
		})
		if err != nil {
			return hooks.ProjectHookDeny
		}
		return decision
	}
}

type hookTrustPopupTarget struct {
	client string
	pane   string
}

func runTmuxHookTrustPopup(ctx context.Context, runner tmuxRunner, binaryPath string, req hooks.ProjectHookPromptRequest, target hookTrustPopupTarget) (hooks.ProjectHookDecision, error) {
	requestFile, err := os.CreateTemp("", "projmux-hook-trust-request-*.json")
	if err != nil {
		return hooks.ProjectHookDeny, err
	}
	requestPath := requestFile.Name()
	defer os.Remove(requestPath)
	defer requestFile.Close()

	encoder := json.NewEncoder(requestFile)
	if err := encoder.Encode(req); err != nil {
		return hooks.ProjectHookDeny, err
	}
	if err := requestFile.Chmod(0o600); err != nil {
		return hooks.ProjectHookDeny, err
	}
	if err := requestFile.Close(); err != nil {
		return hooks.ProjectHookDeny, err
	}

	decisionFile, err := os.CreateTemp("", "projmux-hook-trust-decision-*.txt")
	if err != nil {
		return hooks.ProjectHookDeny, err
	}
	decisionPath := decisionFile.Name()
	defer os.Remove(decisionPath)
	if err := decisionFile.Chmod(0o600); err != nil {
		_ = decisionFile.Close()
		return hooks.ProjectHookDeny, err
	}
	if err := decisionFile.Close(); err != nil {
		return hooks.ProjectHookDeny, err
	}

	args, err := buildHookTrustPopupArgs(binaryPath, requestPath, decisionPath, target)
	if err != nil {
		return hooks.ProjectHookDeny, err
	}
	if _, err := runner.Run(ctx, "tmux", args...); err != nil {
		return hooks.ProjectHookDeny, err
	}

	rawDecision, err := os.ReadFile(decisionPath)
	if err != nil {
		return hooks.ProjectHookDeny, err
	}
	decision := hookcmd.ParseTrustDecision(string(rawDecision))
	if decision == "" {
		return hooks.ProjectHookDeny, nil
	}
	return decision, nil
}

func buildHookTrustPopupArgs(binaryPath, requestPath, decisionPath string, target hookTrustPopupTarget) ([]string, error) {
	binaryPath = strings.TrimSpace(binaryPath)
	requestPath = strings.TrimSpace(requestPath)
	decisionPath = strings.TrimSpace(decisionPath)
	if binaryPath == "" {
		return nil, errors.New("hook trust popup binary path is required")
	}
	if requestPath == "" {
		return nil, errors.New("hook trust popup request path is required")
	}
	if decisionPath == "" {
		return nil, errors.New("hook trust popup decision path is required")
	}
	command := strings.Join([]string{
		keybinding.TmuxShellQuote(binaryPath),
		"internal",
		"tmux",
		"hook-trust-prompt",
		"--request",
		keybinding.TmuxShellQuote(requestPath),
		"--decision",
		keybinding.TmuxShellQuote(decisionPath),
	}, " ")
	return inttmux.BuildDisplayPopupArgs(command, inttmux.PopupOptions{
		Client:        strings.TrimSpace(target.client),
		Target:        strings.TrimSpace(target.pane),
		CloseBehavior: inttmux.PopupCloseOnExit,
		Width:         hookTrustPopupWidth,
		Height:        hookTrustPopupHeight,
		Title:         hookTrustPopupTitle,
	})
}

func (c *tmuxCommand) runHookTrustPrompt(args []string, stdout, stderr io.Writer) error {
	return c.runHookTrustPromptWithReader(args, os.Stdin, stdout, stderr)
}

func (c *tmuxCommand) runHookTrustPromptWithReader(args []string, reader io.Reader, stdout, stderr io.Writer) error {
	// Bright Phase 2 (B3): the hook-trust popup renders with the resolved
	// effective theme instead of the fallback literals.
	defer applyNativeUIThemeFromConfig(c.homeDir, c.lookupEnv, "")()
	fs := flag.NewFlagSet("tmux hook-trust-prompt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	requestPath := fs.String("request", "", "path to project hook trust request JSON")
	decisionPath := fs.String("decision", "", "path to write the selected trust decision")
	if err := fs.Parse(args); err != nil {
		return flagParseReported(err)
	}
	if fs.NArg() != 0 || strings.TrimSpace(*requestPath) == "" || strings.TrimSpace(*decisionPath) == "" {
		return errors.New("tmux hook-trust-prompt requires --request <path> --decision <path>")
	}

	rawRequest, err := os.ReadFile(*requestPath)
	if err != nil {
		return fmt.Errorf("read hook trust request: %w", err)
	}
	var req hooks.ProjectHookPromptRequest
	if err := json.Unmarshal(rawRequest, &req); err != nil {
		return fmt.Errorf("parse hook trust request: %w", err)
	}
	decision := hookcmd.TrustPrompt(reader, stdout, req)
	if err := os.WriteFile(*decisionPath, []byte(string(decision)+"\n"), 0o600); err != nil {
		return fmt.Errorf("write hook trust decision: %w", err)
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
