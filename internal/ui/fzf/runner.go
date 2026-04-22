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
}

type Entry struct {
	Label string
	Value string
}

type Runner interface {
	Run(options Options) (string, error)
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

func (r *runner) Run(options Options) (string, error) {
	if r == nil {
		return "", fmt.Errorf("fzf runner is not configured")
	}

	path, err := r.lookupPath(binaryName)
	if err != nil {
		return "", fmt.Errorf("fzf is not available: %w", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	cmd := r.newCommand(path, runnerArgs(options.UI)...)
	cmd.SetStdin(strings.NewReader(strings.Join(renderedEntries(options), "\n")))
	cmd.SetStdout(&stdout)
	cmd.SetStderr(&stderr)
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", fmt.Errorf("run fzf: %w", err)
		}
		return "", fmt.Errorf("run fzf: %w: %s", err, msg)
	}

	return selectedValue(trimTrailingNewlines(stdout.String())), nil
}

func runnerArgs(ui string) []string {
	return []string{"--prompt", fmt.Sprintf("projmux %s> ", ui), "--delimiter", "\t", "--with-nth", "1"}
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
