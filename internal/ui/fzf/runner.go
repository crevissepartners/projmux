package fzf

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
)

const binaryName = "fzf"

type Options struct {
	UI             string
	Candidates     []string
	Entries        []Entry
	Prompt         string
	Header         string
	Footer         string
	ExpectKeys     []string
	PreviewCommand string
	PreviewWindow  string
	Bindings       []string
}

type Entry struct {
	Label      string
	Value      string
	SearchText string
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
	cmd.SetStdin(strings.NewReader(strings.Join(renderedEntries(options), "\n")))
	cmd.SetStdout(&stdout)
	cmd.SetStderr(&stderr)
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return Result{}, fmt.Errorf("run fzf: %w", err)
		}
		return Result{}, fmt.Errorf("run fzf: %w: %s", err, msg)
	}

	return selectedResult(trimTrailingNewlines(stdout.String()), len(options.ExpectKeys) != 0), nil
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
		"--nth", searchNth(options),
		"--exit-0",
		"--scrollbar", "█",
		"--scroll-off", strconv.Itoa(3),
		"--info", "inline-right",
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

func trimTrailingNewlines(s string) string {
	return strings.TrimRight(s, "\r\n")
}

func renderedEntries(options Options) []string {
	if len(options.Entries) != 0 {
		lines := make([]string, 0, len(options.Entries))
		for _, entry := range options.Entries {
			line := sanitizeField(entry.Label) + "\t" + sanitizeField(entry.Value)
			if usesSearchText(options) {
				line += "\t" + sanitizeField(searchTextForEntry(entry))
			}
			lines = append(lines, line)
		}
		return lines
	}

	lines := make([]string, 0, len(options.Candidates))
	for _, candidate := range options.Candidates {
		lines = append(lines, candidate+"\t"+candidate)
	}
	return lines
}

func searchNth(options Options) string {
	if usesSearchText(options) {
		return "3"
	}
	return "1"
}

func usesSearchText(options Options) bool {
	for _, entry := range options.Entries {
		if strings.TrimSpace(entry.SearchText) != "" {
			return true
		}
	}
	return false
}

func searchTextForEntry(entry Entry) string {
	if strings.TrimSpace(entry.SearchText) != "" {
		return entry.SearchText
	}
	return entry.Label
}

func sanitizeField(value string) string {
	value = strings.ReplaceAll(value, "\t", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}

func selectedResult(selection string, hasExpectKeys bool) Result {
	if !hasExpectKeys {
		return Result{Value: selectedValue(selection)}
	}

	key, selected, ok := strings.Cut(selection, "\n")
	if !ok {
		return Result{Key: strings.TrimSpace(key)}
	}

	return Result{
		Key:   strings.TrimSpace(key),
		Value: selectedValue(selected),
	}
}

func selectedValue(selection string) string {
	fields := strings.Split(selection, "\t")
	if len(fields) < 2 {
		return selection
	}
	return fields[1]
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
