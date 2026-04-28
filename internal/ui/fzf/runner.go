package fzf

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
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
	// AcceptQuery surfaces the user-typed query alongside any selection.
	// When true, the runner passes --print-query to fzf and Result.Query is
	// populated from the first stdout line emitted by fzf. Existing callers
	// can leave this false to keep the previous behavior.
	AcceptQuery bool
}

type Entry struct {
	Label     string
	Value     string
	SearchKey string
}

type Result struct {
	Key   string
	Value string
	// Query is the user-typed query string when Options.AcceptQuery is true.
	// It is empty for callers that do not opt into query echo.
	Query string
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

	input := renderedInput(options)
	filterFile, cleanup, err := searchKeyFilterFile(options, input)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()

	cmd := r.newCommand(path, runnerArgs(options, supportsFooter, filterFile)...)
	cmd.SetStdin(strings.NewReader(input))
	cmd.SetStdout(&stdout)
	cmd.SetStderr(&stderr)
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return Result{}, fmt.Errorf("run fzf: %w", err)
		}
		return Result{}, fmt.Errorf("run fzf: %w: %s", err, msg)
	}

	output := trimTrailingRecordTerminators(stdout.String())
	if options.AcceptQuery {
		return selectedResultWithQuery(output, options.ExpectKeys), nil
	}
	return selectedResult(output, options.ExpectKeys), nil
}

// selectedResultWithQuery handles the --print-query stdout shape: fzf always
// prints the typed query as the first line, then the expect-key (if any) and
// the selected entry on subsequent lines. We split the leading query line off,
// then defer to selectedResult for the remainder.
func selectedResultWithQuery(selection string, expectKeys []string) Result {
	if selection == "" {
		return Result{}
	}
	cutAt := -1
	for _, separator := range []string{"\n", "\x00"} {
		if idx := strings.Index(selection, separator); idx >= 0 && (cutAt < 0 || idx < cutAt) {
			cutAt = idx
		}
	}
	if cutAt < 0 {
		// Single line: query only, no selection (e.g. user typed and pressed Enter
		// against an empty list with --exit-0).
		return Result{Query: selection}
	}
	query := selection[:cutAt]
	rest := selection[cutAt+1:]
	if rest == "" {
		return Result{Query: query}
	}
	result := selectedResult(rest, expectKeys)
	result.Query = query
	return result
}

func runnerArgs(options Options, supportsFooter bool, filterFile string) []string {
	searchKeyed := hasSearchKey(options)
	args := []string{
		"--prompt", resolvedPrompt(options),
		"--height", "100%",
		"--layout", "reverse",
		"--border",
		"--ansi",
		"--delimiter", "\t",
	}
	if searchKeyed {
		args = append(args, "--nth", "1", "--with-nth", "2")
	} else {
		args = append(args, "--with-nth", "1")
	}
	if filterFile != "" {
		args = append(args, "--disabled", "--bind", "change:reload("+searchKeyFilterCommand(filterFile)+")")
	}
	args = append(args,
		"--exit-0",
		"--scrollbar", "█",
		"--info", "inline-right",
	)
	if options.AcceptQuery {
		args = append(args, "--print-query")
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
		if searchKeyed {
			previewCommand = searchKeyValuePlaceholder(previewCommand)
		}
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
		if searchKeyed {
			binding = searchKeyValuePlaceholder(binding)
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
		searchKeyed := hasSearchKey(options)
		lines := make([]string, 0, len(options.Entries))
		for _, entry := range options.Entries {
			if searchKeyed {
				searchKey := strings.TrimSpace(entry.SearchKey)
				if searchKey == "" {
					searchKey = firstLine(entry.Label)
				}
				lines = append(lines, searchKey+"\t"+entry.Label+"\t"+entry.Value)
				continue
			}
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
	if idx := strings.LastIndex(selection, "\t"); idx >= 0 {
		return selection[idx+1:]
	}
	return selection
}

func hasSearchKey(options Options) bool {
	for _, entry := range options.Entries {
		if strings.TrimSpace(entry.SearchKey) != "" {
			return true
		}
	}
	return false
}

func firstLine(value string) string {
	value = strings.TrimSpace(value)
	if before, _, ok := strings.Cut(value, "\n"); ok {
		return strings.TrimSpace(before)
	}
	return value
}

func searchKeyFilterFile(options Options, input string) (string, func(), error) {
	if !hasSearchKey(options) {
		return "", func() {}, nil
	}

	file, err := os.CreateTemp("", "projmux-fzf-*.items")
	if err != nil {
		return "", func() {}, fmt.Errorf("create fzf search-key filter file: %w", err)
	}
	path := file.Name()
	cleanup := func() { _ = os.Remove(path) }

	if _, err := file.WriteString(input); err != nil {
		_ = file.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("write fzf search-key filter file: %w", err)
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("close fzf search-key filter file: %w", err)
	}
	return path, cleanup, nil
}

func searchKeyFilterCommand(path string) string {
	const script = `BEGIN{$q=lc shift}($k)=split(/\t/,$_,2);print if $q eq ""||fuzzy(lc $k,$q);sub fuzzy{my($s,$q)=@_;my$i=0;for my$ch(split//,$q){$i=index($s,$ch,$i);return 0 if $i<0;$i++}1}`
	return "perl -0ne " + shellQuote(script) + " -- {q} " + shellQuote(path)
}

func searchKeyValuePlaceholder(command string) string {
	return strings.ReplaceAll(command, "{2}", "{3}")
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func containsString(values []string, target string) bool {
	return slices.Contains(values, target)
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
