package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/ui/projmuxpicker"
)

const (
	hookTrustPopupTitle           = "Trust project hooks"
	hookTrustPopupWidth           = "90"
	hookTrustPopupHeight          = "24"
	hookTrustPopupContentWidth    = 86
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
				lookupEnv("TMUX_SESSIONIZER_CONTEXT_PANE"),
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
	decision := parseHookTrustDecision(string(rawDecision))
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
		tmuxShellQuote(binaryPath),
		"tmux",
		"hook-trust-prompt",
		"--request",
		tmuxShellQuote(requestPath),
		"--decision",
		tmuxShellQuote(decisionPath),
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
	fs := flag.NewFlagSet("tmux hook-trust-prompt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	requestPath := fs.String("request", "", "path to project hook trust request JSON")
	decisionPath := fs.String("decision", "", "path to write the selected trust decision")
	if err := fs.Parse(args); err != nil {
		return err
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
	decision := hookTrustPopupPrompt(reader, stdout, req)
	if err := os.WriteFile(*decisionPath, []byte(string(decision)+"\n"), 0o600); err != nil {
		return fmt.Errorf("write hook trust decision: %w", err)
	}
	return nil
}

func hookTrustPopupPrompt(reader io.Reader, writer io.Writer, req hooks.ProjectHookPromptRequest) hooks.ProjectHookDecision {
	fmt.Fprintln(writer, projmuxpicker.CurrentStart+" Trust project hooks "+projmuxpicker.Reset)
	fmt.Fprintln(writer, hookTrustMuted("Project-local automation is disabled until this file hash is trusted."))
	fmt.Fprintln(writer)
	writeHookTrustField(writer, "repo", req.RepoPath)
	writeHookTrustField(writer, "hook", req.RelativePath)
	if req.PreviousSHA256 != "" {
		writeHookTrustField(writer, "trusted sha", req.PreviousSHA256)
	}
	writeHookTrustField(writer, "current sha", req.SHA256)
	if strings.TrimSpace(req.Preview) != "" {
		fmt.Fprintln(writer)
		fmt.Fprintln(writer, projmuxpicker.SeparatorLine(hookTrustPopupContentWidth))
		fmt.Fprintln(writer, hookTrustMuted("preview"))
		for line := range strings.SplitSeq(req.Preview, "\n") {
			fmt.Fprintln(writer, "  "+projmuxpicker.TruncateANSI(line, hookTrustPopupContentWidth-2))
		}
	}

	fmt.Fprintln(writer)
	fmt.Fprintln(writer, projmuxpicker.SeparatorLine(hookTrustPopupContentWidth))
	fmt.Fprintln(writer, hookTrustActionLine("[o] Allow once", "run this time only"))
	fmt.Fprintln(writer, hookTrustActionLine("[a] Allow always", "trust this exact file hash"))
	fmt.Fprintln(writer, hookTrustActionLine("[d] Deny", "skip this hook"))

	input := bufio.NewReader(reader)
	for range 3 {
		fmt.Fprint(writer, "\n"+hookTrustMuted("choice")+"  ")
		line, err := input.ReadString('\n')
		if err != nil && len(line) == 0 {
			fmt.Fprintln(writer)
			return hooks.ProjectHookDeny
		}
		decision := parseHookTrustDecision(line)
		if decision != "" {
			return decision
		}
		fmt.Fprintln(writer, hookTrustMuted("Enter o, a, or d."))
	}
	return hooks.ProjectHookDeny
}

func writeHookTrustField(w io.Writer, label, value string) {
	label = strings.TrimSpace(label)
	value = strings.TrimSpace(value)
	if value == "" {
		value = "-"
	}
	fmt.Fprintf(w, "%s  %s\n",
		hookTrustMuted(fmt.Sprintf("%-11s", label)),
		projmuxpicker.TruncateANSI(value, hookTrustPopupContentWidth-13),
	)
}

func hookTrustActionLine(action, detail string) string {
	return fmt.Sprintf("  %-12s %s", action, hookTrustMuted(detail))
}

func hookTrustMuted(value string) string {
	return projmuxpicker.MutedStart + value + projmuxpicker.Reset
}

func parseHookTrustDecision(value string) hooks.ProjectHookDecision {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "o", "once", string(hooks.ProjectHookAllowOnce):
		return hooks.ProjectHookAllowOnce
	case "a", "always", string(hooks.ProjectHookAllowAlways):
		return hooks.ProjectHookAllowAlways
	case "d", "deny", "n", "no":
		return hooks.ProjectHookDeny
	default:
		return ""
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
