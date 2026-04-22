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
	UI         string
	Candidates []string
	Entries    []Entry
	ExpectKeys []string
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
	lookupPath func(string) (string, error)
	newCommand commandFactory
}

func NewRunner() Runner {
	return &runner{
		lookupPath: exec.LookPath,
		newCommand: newExecCommand,
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

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	cmd := r.newCommand(path, runnerArgs(options.UI, options.ExpectKeys)...)
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

func runnerArgs(ui string, expectKeys []string) []string {
	args := []string{"--prompt", fmt.Sprintf("projmux %s> ", ui), "--delimiter", "\t", "--with-nth", "1"}
	if len(expectKeys) != 0 {
		args = append(args, "--expect", strings.Join(expectKeys, ","))
	}
	return args
}

func trimTrailingNewlines(s string) string {
	return strings.TrimRight(s, "\r\n")
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
	_, value, ok := strings.Cut(selection, "\t")
	if !ok {
		return selection
	}
	return value
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
