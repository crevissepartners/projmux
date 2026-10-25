package app

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/crevissepartners/projmux/internal/cli"
	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

// Invocation-only disclosure, computed by the shared runtime so the CLI and the
// public SDK report the same entries. A value pointer preserves explicit none.
type agentSettingsApplication = mutationruntime.AgentSettingsApplication
type agentSettingApplication = mutationruntime.AgentSettingApplication

type agentApplicationSource struct {
	replyOnly      bool
	nativeReady    bool
	provider, host string
	modelArg       string
	settings       agentSettingsLaunch
	resumed        agentResumeLaunch
	guidance       agentGuidanceLaunch
	links          projectLinksLaunch
	workspace      coremetadata.AgentWorkspace
	annotations    map[string]string
	snapshot       bool // process ordinary resume consumes recorded guidance/rules.
}

func (s agentApplicationSource) shared() mutationruntime.AgentApplicationSource {
	return mutationruntime.AgentApplicationSource{ReplyOnly: s.replyOnly, NativeReady: s.nativeReady, Provider: s.provider, Host: s.host, ModelArg: s.modelArg, Settings: s.settings.shared(), Resumed: s.resumed.shared(), Guidance: s.guidance.shared(), Links: s.links.shared(), Workspace: s.workspace, Annotations: s.annotations, Snapshot: s.snapshot}
}

// Called only after launch preparation/commit succeeds, with the actual values
// of that preparation, never with a restart preflight substituted for them.
func preparedAgentSettingsApplication(s agentApplicationSource) *agentSettingsApplication {
	return mutationruntime.PreparedAgentSettingsApplication(s.shared())
}

// A frozen recipe has committed values but no provider yet. Consuming it later
// reports only facts backed by that frozen command, without consulting the
// current profile. Recorded Model/Effort can predate the command preparation.
func frozenAgentSettingsApplication(record *deferredLaunchRecord, pending bool) *agentSettingsApplication {
	a := &agentSettingsApplication{Entries: make([]agentSettingApplication, 0, 8)}
	for _, setting := range []string{"profile", "model", "effort", "instructions", "agent-guidance", "project-guidance", "link-rules", "workspace"} {
		e := agentSettingApplication{Setting: setting, Status: "pending", Reason: "first-input-required"}
		if !pending {
			e.Status, e.Reason = "delivered", "launch-arguments"
		}
		var v string
		switch setting {
		case "profile":
			v = record.NewAnnotations[coremetadata.AnnotationAgentProfile]
			e.Status, e.Reason = "recorded", "profile-recipe-recorded"
		case "model":
			if !pending {
				e.Status, e.Reason = "unobserved", "application-unobserved"
				a.Entries = append(a.Entries, e)
				continue
			}
			v = record.Model
		case "effort":
			if !pending {
				e.Status, e.Reason = "unobserved", "application-unobserved"
				a.Entries = append(a.Entries, e)
				continue
			}
			v = record.Effort
		case "instructions":
			v = record.NewAnnotations[coremetadata.AnnotationAgentPersona]
		case "workspace":
			v = record.Command.Dir
			if !pending {
				e.Reason = "workspace-resolved"
			}
		default:
			a.Entries = append(a.Entries, e)
			continue
		}
		e.Value = &v
		a.Entries = append(a.Entries, e)
	}
	return a
}

func processAgentSettingsApplication(provider string, settings agentSettingsLaunch, command processhost.Command, annotations map[string]string) *agentSettingsApplication {
	return preparedAgentSettingsApplication(agentApplicationSource{provider: provider, host: "process", settings: settings, annotations: annotations, workspace: coremetadata.AgentWorkspace{CWD: command.Dir}, snapshot: true})
}

func lifecycleText(key string) string {
	return LocalizeText(appLocale(os.UserHomeDir, os.Getenv), i18n.Key("agent.lifecycle."+key), "")
}

// Existing notices are best effort; a new disclosure never changes ownership,
// exit status, or failure recovery. Zero confirmed nonapplications adds no line.
func writeAgentSettingsNotice(w io.Writer, a *agentSettingsApplication) {
	if a == nil || a.NotAppliedCount == 0 {
		return
	}
	var delivered, missing []string
	for _, e := range a.Entries {
		if e.Status == "delivered" {
			delivered = append(delivered, fmt.Sprintf(lifecycleText("delivered-entry"), lifecycleText("setting."+e.Setting), lifecycleText("reason."+e.Reason)))
		}
		if e.Status == "not-applied" {
			missing = append(missing, fmt.Sprintf(lifecycleText("missing"), lifecycleText("setting."+e.Setting), lifecycleText("reason."+e.Reason), lifecycleText("next."+e.Reason)))
		}
	}
	fmt.Fprintf(w, lifecycleText("summary")+"\n", len(delivered), strings.Join(delivered, ", "), a.NotAppliedCount, strings.Join(missing, "; "))
}

func trailingAgentPromptFlag(fs *flag.FlagSet, prompt []string) string {
	if len(prompt) == 0 {
		return ""
	}
	token := prompt[len(prompt)-1]
	if !strings.HasPrefix(token, "-") || strings.HasPrefix(token, "---") || token == "-" || token == "--" {
		return ""
	}
	name, _, _ := strings.Cut(strings.TrimLeft(token, "-"), "=")
	if fs.Lookup(name) == nil {
		return ""
	}
	spelling, _, _ := strings.Cut(token, "=")
	return spelling
}
func writeAgentLifecyclePromptNotice(w io.Writer, token string) {
	if token != "" {
		fmt.Fprintf(w, lifecycleText("prompt")+"\n", token)
	}
}

type agentLifecycleNotices struct {
	trailingPromptFlag string
	foreground         bool
}

func (n agentLifecycleNotices) write(w io.Writer) {
	writeAgentLifecyclePromptNotice(w, n.trailingPromptFlag)
	if n.foreground {
		fmt.Fprintln(w, lifecycleText("foreground"))
	}
}
func (c *agentCommand) lifecycleNotices(fs *flag.FlagSet, prompt []string) agentLifecycleNotices {
	n := agentLifecycleNotices{trailingPromptFlag: trailingAgentPromptFlag(fs, prompt)}
	explicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "host" || f.Name == "detach" {
			explicit = true
		}
	})
	n.foreground = c.defaultProcessForegroundNotice(!explicit)
	return n
}
func (c *agentCommand) defaultProcessForegroundNotice(eligible bool) bool {
	if !eligible || c.rebind == nil || c.rebind.create == nil {
		return false
	}
	creator := c.rebind.create
	paths, err := savedSettingsPaths(creator.homeDir, creator.lookupEnv)
	if err != nil {
		return false
	}
	host, err := config.LoadAgentDefaultHostFile(paths.AgentDefaultHostFile())
	return err == nil && host == config.AgentHostProcess
}

// Preserve the selected mode's display suppression, including new warnings.
func (n agentLifecycleNotices) writeForMode(w io.Writer, mode cli.OutputMode) {
	if mode != cli.OutputModeNone {
		n.write(w)
	}
}

func processResumeSettingsApplication(candidate processResumeCandidate, settings agentSettingsLaunch, command processhost.Command, frozen *deferredLaunchRecord) *agentSettingsApplication {
	if frozen != nil {
		return frozenAgentSettingsApplication(frozen, false)
	}
	return processAgentSettingsApplication(candidate.Record.Provider, settings, command, candidate.Agent.Metadata.Annotations)
}

func writeRelaunchLifecycleNotices(w io.Writer, request agentRelaunchRequest) {
	agentLifecycleNotices{trailingPromptFlag: request.trailingPromptFlag, foreground: request.foregroundNotice && !request.detach && request.host == ""}.write(w)
}

// Only a committed frozen result uses the recorded-settings suffix. Ordinary
// child-free claims retain the six-field ownership line and get a separate,
// best-effort lifetime notice in default mode only.
func writeAgentFirstInputNotice(w io.Writer, defaultOutput bool) {
	if defaultOutput {
		fmt.Fprintln(w, lifecycleText("claim-waiting"))
	}
}
func agentSettingsPendingSuffix(a *agentSettingsApplication) string {
	if a != nil {
		for _, e := range a.Entries {
			if e.Status == "pending" {
				return "; " + lifecycleText("pending")
			}
		}
	}
	return ""
}
