package app

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/services/settings"
)

// runLocale answers `config locale [--set <value>]`.
//
// `[ui] locale` is a central setting: every front renders from it. The route
// reads and stores it through the central settings API (central_settings.go),
// the same functions the Settings locale row calls, so it adds no second
// validator or writer. An unsupported value is refused there before any file
// is touched.
//
// Output is en-US, like `config providers`. The read prints `locale <value>
// from <config.toml path>`; a store prints `locale <value>`.
func (c *configCommand) runLocale(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("config locale", flag.ContinueOnError)
	// The refusal prints the prefixed reason and the catalog Usage. The flag
	// package's own output would put a second, differently worded copy of
	// the reason on stderr.
	fs.SetOutput(io.Discard)
	value := fs.String("set", "", "store the [ui] locale setting")
	if err := fs.Parse(args); err != nil {
		return usageRefusal(stderr, "config locale", "config locale: "+err.Error())
	}
	if fs.NArg() != 0 {
		return usageError(fmt.Sprintf("config locale does not accept positional arguments: %s", strings.Join(fs.Args(), " ")))
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	if !set["set"] {
		setting, source, err := loadCentralLocaleSetting(c.homeDir, c.lookupEnv)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "locale %s from %s\n", setting, source)
		return err
	}
	if strings.TrimSpace(*value) == "" {
		return usageError("config locale --set requires a locale setting")
	}
	saved, err := saveCentralLocale(c.homeDir, c.lookupEnv, *value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "locale %s\n", saved)
	return err
}

// runAgentQuestions answers `config agent-questions [--answering <way>]
// [--window <seconds|unlimited>]`.
//
// Both values are central settings the Claude question hook reads. The route
// reads and stores them through the central settings API, the same files and
// loaders the hook uses. Every given flag is validated before any file is
// written, so a bad `--window` next to a good `--answering` changes nothing.
//
// The answering way is checked against its two words here because
// config.NormalizeAgentQuestionAnswering reads any other word as way 1: that
// suits a hook reading a file, not an operator typing a value. The window
// takes whole seconds in
// config.MinAgentQuestionWindowSeconds..config.MaxAgentQuestionWindowSeconds,
// or config.AgentQuestionWindowUnlimitedWord.
//
// Output is en-US and one line, `answering <way> window <seconds|unlimited>`,
// both for a read and after a store.
func (c *configCommand) runAgentQuestions(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("config agent-questions", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	answering := fs.String("answering", "", "store how agent questions are answered")
	window := fs.String("window", "", "store how long an agent question waits")
	if err := fs.Parse(args); err != nil {
		return usageRefusal(stderr, "config agent-questions", "config agent-questions: "+err.Error())
	}
	if fs.NArg() != 0 {
		return usageError(fmt.Sprintf("config agent-questions does not accept positional arguments: %s", strings.Join(fs.Args(), " ")))
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	if !set["answering"] && !set["window"] {
		_, err := fmt.Fprintf(stdout, "answering %s window %s\n", loadCentralAgentQuestionAnswering(c.homeDir, c.lookupEnv), agentQuestionWindowWord(loadCentralAgentQuestionWindowSeconds(c.homeDir, c.lookupEnv)))
		return err
	}
	update := settings.Update{Key: "agent-questions"}
	if set["answering"] {
		update.Answering = answering
	}
	if set["window"] {
		update.Window = window
	}
	result, err := c.centralPolicyService().Set(context.Background(), update)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "answering %s window %s\n", result.Answering, result.Window)
	return err
}

// agentQuestionWindowWord prints a window the way `--window` accepts it.
func agentQuestionWindowWord(seconds int) string { return settings.AgentQuestionWindowWord(seconds) }

// runAgentApprovals answers `config agent-approvals [--answering <way>]
// [--window <seconds>]`.
//
// Both values are central settings the Claude permission hook reads, separate
// from the agent-questions files. The answering way captures Claude permission
// requests and allows remote (non-interactive) `projmux agent approval answer`
// for both Claude and Codex Agents; the window applies to captured Claude
// requests only, since Codex approvals are held by the provider and never
// captured. `projmux agent approval review` answers a Codex approval in either
// way. Every given flag is validated before any file is written, so
// a bad `--window` next to a good `--answering` changes nothing. The window
// takes whole seconds in
// config.MinAgentApprovalWindowSeconds..config.MaxAgentApprovalWindowSeconds
// and has no unlimited word.
//
// Output is en-US and one line, `answering <way> window <seconds>`, both for a
// read and after a store.
func (c *configCommand) runAgentApprovals(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("config agent-approvals", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	answering := fs.String("answering", "", "store how agent permission requests are answered")
	window := fs.String("window", "", "store how long a captured permission request waits")
	if err := fs.Parse(args); err != nil {
		return usageRefusal(stderr, "config agent-approvals", "config agent-approvals: "+err.Error())
	}
	if fs.NArg() != 0 {
		return usageError(fmt.Sprintf("config agent-approvals does not accept positional arguments: %s", strings.Join(fs.Args(), " ")))
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	if !set["answering"] && !set["window"] {
		_, err := fmt.Fprintf(stdout, "answering %s window %s\n", loadCentralAgentApprovalAnswering(c.homeDir, c.lookupEnv), fmt.Sprint(loadCentralAgentApprovalWindowSeconds(c.homeDir, c.lookupEnv)))
		return err
	}
	update := settings.Update{Key: "agent-approvals"}
	if set["answering"] {
		update.Answering = answering
	}
	if set["window"] {
		update.Window = window
	}
	result, err := c.centralPolicyService().Set(context.Background(), update)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "answering %s window %s\n", result.Answering, result.Window)
	return err
}

// centralPolicyService keeps every front on the central save adapters while
// sharing validation and write ordering with the SDK service.
func (c *configCommand) centralPolicyService() settings.Service {
	return settings.Service{
		Paths: func() (config.Paths, error) { return configPaths(c.homeDir, c.lookupEnv) },
		QuestionAnswering: func(way config.AgentQuestionAnswering) (config.AgentQuestionAnswering, error) {
			return saveCentralAgentQuestionAnswering(c.homeDir, c.lookupEnv, way)
		},
		QuestionWindow: func(seconds int) error { return saveCentralAgentQuestionWindowSeconds(c.homeDir, c.lookupEnv, seconds) },
		ApprovalAnswering: func(way config.AgentApprovalAnswering) error {
			return saveCentralAgentApprovalAnswering(c.homeDir, c.lookupEnv, way)
		},
		ApprovalWindow: func(seconds int) error { return saveCentralAgentApprovalWindowSeconds(c.homeDir, c.lookupEnv, seconds) },
	}
}
