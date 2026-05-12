package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	codexConfigRelativePath = ".codex/config.toml"
	codexNotifyMarkerBegin  = "# >>> projmux managed codex legacy notify"
	codexNotifyMarkerEnd    = "# <<< projmux managed codex legacy notify"
	codexNotifyLine         = `notify = ["projmux", "ai", "ingest", "codex-notify"]`
)

type codexNotifyPlan struct {
	path     string
	current  string
	next     string
	action   string
	changed  bool
	conflict string
}

func (c *aiCommand) runIntegrate(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printAIUsage(stderr)
		return errors.New("ai integrate requires <agent-kind>")
	}
	switch args[0] {
	case "codex":
		return c.runIntegrateCodex(args[1:], stdout, stderr)
	case "help", "--help", "-h":
		printAIUsage(stdout)
		return nil
	default:
		printAIUsage(stderr)
		return fmt.Errorf("unknown ai integrate agent-kind: %s", args[0])
	}
}

func (c *aiCommand) runIntegrateCodex(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("ai integrate codex", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dryRun := fs.Bool("dry-run", false, "print planned Codex notify config changes without writing")
	remove := fs.Bool("remove", false, "remove projmux-managed Codex notify wiring")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		printAIUsage(stderr)
		return errors.New("ai integrate codex does not accept positional arguments")
	}

	plan, err := c.planCodexNotifyIntegration(*remove)
	if err != nil {
		return err
	}
	if *dryRun {
		return printCodexNotifyDryRun(stdout, plan)
	}
	if plan.conflict != "" {
		return errors.New(plan.conflict + "; run `projmux ai integrate codex --dry-run` to preview without writing")
	}
	if !plan.changed {
		_, err := fmt.Fprintf(stdout, "%s\n", plan.action)
		return err
	}
	if err := c.writeCodexConfig(plan.path, []byte(plan.next)); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "%s\n", plan.action)
	return err
}

func (c *aiCommand) planCodexNotifyIntegration(remove bool) (codexNotifyPlan, error) {
	home, err := c.homeDir()
	if err != nil {
		return codexNotifyPlan{}, fmt.Errorf("resolve home directory: %w", err)
	}
	path := filepath.Join(home, codexConfigRelativePath)
	current, err := c.readCodexConfig(path)
	if err != nil {
		return codexNotifyPlan{}, err
	}

	withoutManaged, hadManaged, err := removeCodexNotifyManagedBlocks(current)
	if err != nil {
		return codexNotifyPlan{}, err
	}
	plan := codexNotifyPlan{path: path, current: current, next: withoutManaged}

	if remove {
		plan.changed = hadManaged
		if hadManaged {
			plan.action = "removed projmux-managed Codex legacy notify wiring from " + path
		} else {
			plan.action = "no changes: projmux-managed Codex legacy notify wiring is not present in " + path
		}
		return plan, nil
	}

	if line, ok := findUnmanagedNotifyLine(withoutManaged); ok {
		plan.conflict = fmt.Sprintf("Codex notify is already configured outside a projmux-managed block in %s: %s", path, line)
		plan.action = "would refuse to install Codex legacy notify wiring"
		return plan, nil
	}

	next := codexNotifyBlock() + withoutManaged
	plan.next = next
	plan.changed = next != current
	if plan.changed {
		plan.action = "configured Codex legacy notify in " + path
	} else {
		plan.action = "no changes: Codex legacy notify is already configured in " + path
	}
	return plan, nil
}

func (c *aiCommand) readCodexConfig(path string) (string, error) {
	readFile := c.readFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	data, err := readFile(path)
	if err == nil {
		return string(data), nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return "", fmt.Errorf("read Codex config %s: %w", path, err)
}

func (c *aiCommand) writeCodexConfig(path string, data []byte) error {
	mkdirAll := c.mkdirAll
	if mkdirAll == nil {
		mkdirAll = os.MkdirAll
	}
	if err := mkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create Codex config directory: %w", err)
	}
	writeFile := c.writeFile
	if writeFile == nil {
		writeFile = os.WriteFile
	}
	if err := writeFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write Codex config %s: %w", path, err)
	}
	return nil
}

func printCodexNotifyDryRun(stdout io.Writer, plan codexNotifyPlan) error {
	if _, err := fmt.Fprintf(stdout, "projmux ai integrate codex (dry-run)\nconfig: %s\n", plan.path); err != nil {
		return err
	}
	if plan.conflict != "" {
		_, err := fmt.Fprintf(stdout, "%s\n%s\n", plan.action, plan.conflict)
		return err
	}
	if !plan.changed {
		_, err := fmt.Fprintf(stdout, "%s\n", plan.action)
		return err
	}
	switch {
	case plan.next == "":
		_, err := fmt.Fprintln(stdout, "would remove projmux-managed Codex legacy notify wiring")
		return err
	case plan.current == "":
		_, err := fmt.Fprintf(stdout, "would create config with managed block:\n%s", codexNotifyBlock())
		return err
	default:
		_, err := fmt.Fprintf(stdout, "would update config to:\n%s", plan.next)
		return err
	}
}

func codexNotifyBlock() string {
	return strings.Join([]string{
		codexNotifyMarkerBegin,
		codexNotifyLine,
		codexNotifyMarkerEnd,
	}, "\n") + "\n"
}

func removeCodexNotifyManagedBlocks(content string) (string, bool, error) {
	lines := strings.SplitAfter(content, "\n")
	var out strings.Builder
	removed := false
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != codexNotifyMarkerBegin {
			out.WriteString(lines[i])
			continue
		}
		removed = true
		foundEnd := false
		for i++; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == codexNotifyMarkerEnd {
				foundEnd = true
				break
			}
		}
		if !foundEnd {
			return "", false, errors.New("Codex config contains an unterminated projmux-managed notify block")
		}
	}
	return out.String(), removed, nil
}

func findUnmanagedNotifyLine(content string) (string, bool) {
	for line := range strings.SplitSeq(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(trimmed, "notify"); ok && strings.HasPrefix(strings.TrimSpace(rest), "=") {
			return trimmed, true
		}
	}
	return "", false
}
