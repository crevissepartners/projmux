package app

import (
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
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
	// The usage error carries the message. The flag package's own usage dump
	// would put a second, differently worded copy on stderr.
	fs.SetOutput(io.Discard)
	value := fs.String("set", "", "store the [ui] locale setting")
	if err := fs.Parse(args); err != nil {
		return usageError("config locale: " + err.Error())
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
// takes the same spellings as the Settings custom row: whole seconds in
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
		return usageError("config agent-questions: " + err.Error())
	}
	if fs.NArg() != 0 {
		return usageError(fmt.Sprintf("config agent-questions does not accept positional arguments: %s", strings.Join(fs.Args(), " ")))
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	var way config.AgentQuestionAnswering
	if set["answering"] {
		parsed, err := knownAgentQuestionAnswering(*answering)
		if err != nil {
			return err
		}
		way = parsed
	}
	var seconds int
	if set["window"] {
		if strings.TrimSpace(*window) == "" {
			return usageError("config agent-questions --window requires a value: " + agentQuestionWindowSpellings())
		}
		parsed, err := parseAgentQuestionWindow(*window)
		if err != nil {
			return usageError(fmt.Sprintf("config agent-questions --window: invalid window %q; want %s", *window, agentQuestionWindowSpellings()))
		}
		seconds = parsed
	}

	if set["answering"] {
		if _, err := saveCentralAgentQuestionAnswering(c.homeDir, c.lookupEnv, way); err != nil {
			return err
		}
	}
	if set["window"] {
		if err := saveCentralAgentQuestionWindowSeconds(c.homeDir, c.lookupEnv, seconds); err != nil {
			return err
		}
	}
	// Report what the hook will read, not what was asked for.
	_, err := fmt.Fprintf(stdout, "answering %s window %s\n",
		loadCentralAgentQuestionAnswering(c.homeDir, c.lookupEnv),
		agentQuestionWindowWord(loadCentralAgentQuestionWindowSeconds(c.homeDir, c.lookupEnv)))
	return err
}

// knownAgentQuestionAnswering resolves one operator-typed answering way,
// refusing any word other than the two ways.
func knownAgentQuestionAnswering(value string) (config.AgentQuestionAnswering, error) {
	known := []config.AgentQuestionAnswering{config.AgentQuestionAnsweringClaude, config.AgentQuestionAnsweringProjmux}
	names := make([]string, 0, len(known))
	for _, way := range known {
		names = append(names, string(way))
	}
	if strings.TrimSpace(value) == "" {
		return "", usageError("config agent-questions --answering requires a way: " + strings.Join(names, ", "))
	}
	typed := config.AgentQuestionAnswering(strings.ToLower(strings.TrimSpace(value)))
	for _, way := range known {
		if typed == way {
			return way, nil
		}
	}
	return "", usageError(fmt.Sprintf("config agent-questions --answering: unknown way %q; known ways: %s", value, strings.Join(names, ", ")))
}

func agentQuestionWindowSpellings() string {
	return fmt.Sprintf("%d..%d seconds or %s", config.MinAgentQuestionWindowSeconds, config.MaxAgentQuestionWindowSeconds, config.AgentQuestionWindowUnlimitedWord)
}

// agentQuestionWindowWord prints a window the way `--window` accepts it.
func agentQuestionWindowWord(seconds int) string {
	if seconds == config.UnlimitedAgentQuestionWindowSeconds {
		return config.AgentQuestionWindowUnlimitedWord
	}
	return strconv.Itoa(seconds)
}
