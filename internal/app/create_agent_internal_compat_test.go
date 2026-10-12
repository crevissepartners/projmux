package app

import mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"

func activationUnconfirmedDiagnostic(target agentActivationTarget, reason string) string {
	return mutationruntime.ActivationUnconfirmedDiagnostic(target.shared(), reason)
}

func activationUnconfirmedDiagnosticSteps(target agentActivationTarget) []string {
	return mutationruntime.ActivationUnconfirmedDiagnosticSteps(target.shared())
}

// changed reports that the Agent's recorded digest is not the current one:
// guidance added, changed or turned off.
func (l agentGuidanceLaunch) changed() bool { return l.shared().Changed() }

func (c *aiCommand) agentLaunchCommandForArgv(mode, pathPrepend, contextDir, title string, execArgv []string) string {
	return c.sharedAgentCommand().AgentLaunchCommandForArgv(mode, pathPrepend, contextDir, title, execArgv)
}

// codexAgentIdentity is the paragraph that tells a Codex Agent its own Agent
// UID and how to name itself and its targets: a Codex shell command runs in
// the shared app server, not in the Agent's pane, so projmux cannot infer
// either from the environment.
func codexAgentIdentity(agentUID string) string { return mutationruntime.CodexAgentIdentity(agentUID) }

func localProviderGrammar(g mutationruntime.ProviderLaunchGrammar) providerLaunchGrammar {
	return providerLaunchGrammar{g.WorkingRootFlag, g.AdditionalRootFlag, providerOptionArity(g.AdditionalRootArity), g.PayloadTerminator}
}

func (l projectGuidanceLaunch) on() bool { return l.shared().On() }

// providerLaunchGrammarFor returns the argv grammar of one provider.
//
// Measured against the installed CLIs on 2026-08-19:
//
//   - Claude Code 2.1.234 documents `--add-dir <directories...>` and
//     `claude [options] [command] [prompt]`. `claude --add-dir DIR hello` was
//     observed rejecting the run with "Input must be provided either through
//     stdin or as a prompt argument", i.e. `hello` reached the parser as a
//     directory and no prompt survived; `claude --add-dir DIR -- hello` and
//     `claude --add-dir A B -- hello` both reached execution with the prompt
//     intact. That pair is the whole reason this file exists.
//   - Codex documents `-C, --cd <DIR>` and `--add-dir <DIR>`, each taking
//     exactly one value, with `codex [OPTIONS] [PROMPT]`. Its parser would
//     accept `--` as well, but Codex's argv is deliberately left byte-identical
//     to what shipped, because a one-value option cannot absorb the payload and
//     there is nothing here to repair.
//   - Antigravity's `agy` does have a repeatable `--add-dir`, but projmux does
//     not give it additional roots: `resolveAgentWorkspaceFor` refuses them for
//     every provider except Codex and Claude, and widening that is a product
//     decision rather than part of this repair. The empty grammar below records
//     that policy, so a stored root that outlived the gate refuses instead of
//     reaching `agy` through a flag this seam never validated.
func providerLaunchGrammarFor(provider string) providerLaunchGrammar {
	return localProviderGrammar(mutationruntime.ProviderLaunchGrammarFor(provider))
}

// cliSplitCWDSource resolves where a CLI split starts: `--cwd-from` alone.
//
// A CLI result is determined by its arguments. `create pane`, `create agent`
// and the provider shortcuts therefore open no config file at all on this
// route, and with no flag every CLI split starts in the owner Project root.
// Scripts and automation must not change behavior because a human changed a
// Settings value, so the `[ai] split_cwd_from` tiers below are deliberately
// out of reach here. `--cwd-from pane` still obeys the owner Project root rule
// and still prints the fallback notice.
func cliSplitCWDSource(flagValue string) splitCWDSource {
	if source, ok := parseSplitCWDSource(flagValue); ok {
		return source
	}
	return splitCWDFromProject
}
