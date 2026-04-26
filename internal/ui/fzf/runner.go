package fzf

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

const binaryName = "fzf"

type Options struct {
	UI             string
	Candidates     []string
	Entries        []Entry
	Read0          bool
	Prompt         string
	Header         string
	Footer         string
	ExpectKeys     []string
	PreviewCommand string
	PreviewWindow  string
	Bindings       []string
}

type Entry struct {
	Label string
	Value string
}

type Result struct {
	Key   string
	Value string
}

type Runner interface {
	Run(options Options) (Result, error)
}

type command interface {
	SetStdin(io.Reader)
	SetStdout(io.Writer)
	SetStderr(io.Writer)
	Run() error
}

type commandFactory func(name string, args ...string) command

type runner struct {
	lookupPath     func(string) (string, error)
	newCommand     commandFactory
	supportsFooter func(string) bool
}

func NewRunner() Runner {
	return &runner{
		lookupPath:     exec.LookPath,
		newCommand:     newExecCommand,
		supportsFooter: defaultSupportsFooter,
	}
}

func (r *runner) Run(options Options) (Result, error) {
	if r == nil {
		return Result{}, fmt.Errorf("fzf runner is not configured")
	}

	path, err := r.lookupPath(binaryName)
	if err != nil {
		return Result{}, fmt.Errorf("fzf is not available: %w", err)
	}
	supportsFooter := false
	if r.supportsFooter != nil {
		supportsFooter = r.supportsFooter(path)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	cmd := r.newCommand(path, runnerArgs(options, supportsFooter)...)
	cmd.SetStdin(strings.NewReader(renderedInput(options)))
	cmd.SetStdout(&stdout)
	cmd.SetStderr(&stderr)
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return Result{}, fmt.Errorf("run fzf: %w", err)
		}
		return Result{}, fmt.Errorf("run fzf: %w: %s", err, msg)
	}

	return selectedResult(trimTrailingRecordTerminators(stdout.String()), options.ExpectKeys), nil
}

func runnerArgs(options Options, supportsFooter bool) []string {
	args := []string{
		"--prompt", resolvedPrompt(options),
		"--height", "100%",
		"--layout", "reverse",
		"--border",
		"--ansi",
		"--delimiter", "\t",
		"--with-nth", "1",
		"--exit-0",
		"--scrollbar", "█",
		"--info", "inline-right",
	}
	if options.Read0 {
		args = append(args,
			"--read0",
			"--print0",
			"--highlight-line",
			"--gap",
			"--gap-line", "─",
			"--pointer", "▌",
			"--marker-multi-line", "┃┃┃",
			"--color", "current-bg:#263238,current-fg:#ffffff,current-hl:#ffcc66,selected-bg:#1f292d,gutter:#263238,pointer:#e12672,marker:#e12672",
		)
	}
	if len(options.ExpectKeys) != 0 {
		args = append(args, "--expect", strings.Join(options.ExpectKeys, ","))
	}
	if header := strings.TrimSpace(options.Header); header != "" {
		args = append(args, "--header", header)
	}
	if footer := strings.TrimSpace(options.Footer); footer != "" {
		if supportsFooter {
			args = append(args, "--footer", footer, "--footer-border", "line")
		} else {
			args = append(args, "--header", footer, "--separator", "─")
		}
	}
	if previewCommand := strings.TrimSpace(options.PreviewCommand); previewCommand != "" {
		args = append(args, "--preview", previewCommand)
		if previewWindow := strings.TrimSpace(options.PreviewWindow); previewWindow != "" {
			args = append(args, "--preview-window", previewWindow)
		}
	}
	for _, binding := range options.Bindings {
		binding = strings.TrimSpace(binding)
		if binding == "" {
			continue
		}
		args = append(args, "--bind", binding)
	}
	return args
}

func resolvedPrompt(options Options) string {
	if options.Prompt != "" {
		return options.Prompt
	}
	return fmt.Sprintf("projmux %s> ", options.UI)
}

func defaultSupportsFooter(path string) bool {
	out, err := exec.Command(path, "--help").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "--footer")
}

func trimTrailingRecordTerminators(s string) string {
	return strings.TrimRight(s, "\x00\r\n")
}

func renderedInput(options Options) string {
	separator := "\n"
	if options.Read0 {
		separator = "\x00"
	}
	return strings.Join(renderedEntries(options), separator)
}

func renderedEntries(options Options) []string {
	if len(options.Entries) != 0 {
		lines := make([]string, 0, len(options.Entries))
		for _, entry := range options.Entries {
			lines = append(lines, entry.Label+"\t"+entry.Value)
		}
		return lines
	}

	lines := make([]string, 0, len(options.Candidates))
	for _, candidate := range options.Candidates {
		lines = append(lines, candidate+"\t"+candidate)
	}
	return lines
}

func selectedResult(selection string, expectKeys []string) Result {
	if len(expectKeys) == 0 {
		return Result{Value: selectedValue(selection)}
	}

	if key, selected, ok := cutExpectedKey(selection, expectKeys); ok {
		return Result{
			Key:   key,
			Value: selectedValue(selected),
		}
	}

	return Result{Value: selectedValue(selection)}
}

func cutExpectedKey(selection string, expectKeys []string) (string, string, bool) {
	cutAt := -1
	for _, separator := range []string{"\n", "\x00"} {
		if idx := strings.Index(selection, separator); idx >= 0 && (cutAt < 0 || idx < cutAt) {
			cutAt = idx
		}
	}
	if cutAt < 0 {
		return "", "", false
	}

	key := strings.TrimSpace(selection[:cutAt])
	if key == "" || containsString(expectKeys, key) {
		return key, selection[cutAt+1:], true
	}
	return "", "", false
}

func selectedValue(selection string) string {
	_, value, ok := strings.Cut(selection, "\t")
	if !ok {
		return selection
	}
	return value
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

type execCommand struct {
	cmd *exec.Cmd
}

func newExecCommand(name string, args ...string) command {
	return &execCommand{cmd: exec.Command(name, args...)}
}

func (c *execCommand) SetStdin(r io.Reader) {
	c.cmd.Stdin = r
}

func (c *execCommand) SetStdout(w io.Writer) {
	c.cmd.Stdout = w
}

func (c *execCommand) SetStderr(w io.Writer) {
	c.cmd.Stderr = w
}

func (c *execCommand) Run() error {
	return c.cmd.Run()
}
