package projmux

import (
	"context"
	"errors"
	"maps"
	"os"
	"strings"
	"syscall"

	"github.com/crevissepartners/projmux/internal/config"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	links "github.com/crevissepartners/projmux/internal/core/projectlinks"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	service "github.com/crevissepartners/projmux/internal/services/projectlinks"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

const CodeInvalidLinkRules Code = "invalid-link-rules"
const CodeLinkRulesUnreadable Code = "link-rules-unreadable"
const CodeLinkRulesConfirmationRequired Code = "link-rules-confirmation-required"

func (c *Client) linkRulesService() service.Service {
	return service.Service{Paths: c.instructions.Paths, RegistryPaths: func() (config.Paths, error) { return c.paths, c.pathsErr }}
}

// ReadProjectLinkRules reads rules for an explicit Project name or uid: ref.
// Missing rule files yield empty rules; malformed stored files are typed refusals.
func (c *Client) ReadProjectLinkRules(ctx context.Context, projectRef string) (core.ProjectLinkRulesResult, error) {
	return c.ReadProjectLinkRulesWithOptions(ctx, projectRef, core.ProjectLinkRulesOptions{})
}

// ReadProjectLinkRulesWithOptions is ReadProjectLinkRules with call-specific
// Registry admission. AllowDegradedRegistry permits a uniquely identified Project
// in a decodable Registry whose graph is invalid, without repairing Registry state.
func (c *Client) ReadProjectLinkRulesWithOptions(ctx context.Context, projectRef string, options core.ProjectLinkRulesOptions) (core.ProjectLinkRulesResult, error) {
	out, err := c.linkRulesService().ReadWithOptions(ctx, projectRef, service.Options{AllowDegradedRegistry: options.AllowDegradedRegistry})
	return publicLinkRules(out), linkRulesRefusal(err, "ReadProjectLinkRules projectRef")
}

// statLinkRulesFile is replaced only by package tests.
var statLinkRulesFile = linkRulesFileIdentity

// ReadProjectLinkRulesByUID reads the stored link rules of the bare Project UID
// projectUID from the captured configuration directory, with the identity of
// the file they were read from. It reads no Registry and does not check that
// the Project exists, so a missing or malformed Registry does not affect it.
//
// The file is read as ReadProjectLinkRules reads it: a missing file (or
// directory) is the zero rules with a nil identity and no error, and nothing
// is created, locked or repaired. A malformed, unreadable, non-regular,
// symlinked-away or oversized file is a CodeLinkRulesUnreadable refusal whose
// errors.Is and errors.As reach the read's cause. An empty or prefixed UID, or
// one that is not Project UID shaped, is a CodeSelectorInvalid refusal.
// FileIdentity is set only when the stats taken immediately before and after
// the read find the same regular file. Cancellation is checked on entry and
// before returning success; a read failure takes precedence over it. The
// rules belong to the caller.
func (c *Client) ReadProjectLinkRulesByUID(ctx context.Context, projectUID string) (core.ProjectLinkRulesRead, error) {
	if err := ctx.Err(); err != nil {
		return core.ProjectLinkRulesRead{}, err
	}
	if !metadata.IsProjectUIDShaped(projectUID) {
		return core.ProjectLinkRulesRead{}, &Refusal{Code: CodeSelectorInvalid, message: "ReadProjectLinkRulesByUID projectUID must be a bare Project UID"}
	}
	paths, err := c.instructions.Paths()
	if err != nil {
		return core.ProjectLinkRulesRead{}, err
	}
	store := links.NewDefaultStore(paths)
	path, err := store.Path(projectUID)
	if err != nil {
		return core.ProjectLinkRulesRead{}, err
	}
	before, stable := statLinkRulesFile(path)
	rules, err := store.Load(projectUID)
	if err != nil {
		refusal := &service.Refusal{Code: service.CodeUnreadable, Reason: err.Error(), Cause: err}
		return core.ProjectLinkRulesRead{}, &Refusal{Code: CodeLinkRulesUnreadable, Reason: refusal.Reason, message: refusal.Error(), cause: err}
	}
	var identity *core.FileIdentity
	if after, ok := statLinkRulesFile(path); stable && ok && after == before {
		identity = &after
	}
	if err := ctx.Err(); err != nil {
		return core.ProjectLinkRulesRead{}, err
	}
	out := core.ProjectLinkRulesRead{Rules: core.ProjectLinkRules{Jira: rules.Jira, Repo: rules.Repo, URLs: rules.URLs}, FileIdentity: identity}
	for _, link := range rules.Links {
		out.Rules.Links = append(out.Rules.Links, core.ProjectLink{LabelKey: link.LabelKey, Template: link.Template})
	}
	return out, nil
}

// linkRulesFileIdentity is the stat identity of the regular file at path
// itself; ok is false when it cannot be stat'ed or is not a regular file.
func linkRulesFileIdentity(path string) (core.FileIdentity, bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return core.FileIdentity{}, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return core.FileIdentity{}, false
	}
	seconds, nanoseconds := localipc.StatChangeTime(stat)
	return core.FileIdentity{Device: uint64(stat.Dev), Inode: stat.Ino, Size: info.Size(),
		ModTimeNanoseconds: info.ModTime().UnixNano(), ChangeTimeSeconds: seconds, ChangeTimeNanoseconds: nanoseconds}, true
}

// WriteProjectLinkRules replaces the whole file atomically, or validates and
// previews without writing when dryRun is true. Pre-admission cancellation
// writes nothing; admitted writes finish. Concurrent edits are not serialized.
func (c *Client) WriteProjectLinkRules(ctx context.Context, projectRef string, rules core.ProjectLinkRules, dryRun bool) (core.ProjectLinkRulesResult, error) {
	return c.WriteProjectLinkRulesWithOptions(ctx, projectRef, rules, dryRun, core.ProjectLinkRulesOptions{})
}

// WriteProjectLinkRulesWithOptions is WriteProjectLinkRules with call-specific
// Registry admission. AllowDegradedRegistry permits a uniquely identified Project
// in a decodable Registry whose graph is invalid, without repairing Registry state.
func (c *Client) WriteProjectLinkRulesWithOptions(ctx context.Context, projectRef string, rules core.ProjectLinkRules, dryRun bool, options core.ProjectLinkRulesOptions) (core.ProjectLinkRulesResult, error) {
	input := links.Rules{Jira: append([]string(nil), rules.Jira...), Repo: append([]string(nil), rules.Repo...), URLs: maps.Clone(rules.URLs)}
	for _, link := range rules.Links {
		input.Links = append(input.Links, links.Link{LabelKey: link.LabelKey, Template: link.Template})
	}
	out, err := c.linkRulesService().WriteWithOptions(ctx, projectRef, input, dryRun, service.Options{AllowDegradedRegistry: options.AllowDegradedRegistry})
	return publicLinkRules(out), linkRulesRefusal(err, "WriteProjectLinkRules projectRef")
}

// DeleteProjectLinkRules removes even an unreadable file. Missing files are
// already deleted. The result is the Project's empty rules and available variables.
// It requires confirm=true, equivalent to CLI --yes; false deletes nothing.
func (c *Client) DeleteProjectLinkRules(ctx context.Context, projectRef string, confirm bool) (core.ProjectLinkRulesResult, error) {
	return c.DeleteProjectLinkRulesWithOptions(ctx, projectRef, confirm, core.ProjectLinkRulesOptions{})
}

// DeleteProjectLinkRulesWithOptions is DeleteProjectLinkRules with call-specific
// Registry admission. AllowDegradedRegistry permits a uniquely identified Project
// in a decodable Registry whose graph is invalid, without repairing Registry state.
func (c *Client) DeleteProjectLinkRulesWithOptions(ctx context.Context, projectRef string, confirm bool, options core.ProjectLinkRulesOptions) (core.ProjectLinkRulesResult, error) {
	out, err := c.linkRulesService().DeleteWithOptions(ctx, projectRef, confirm, service.Options{AllowDegradedRegistry: options.AllowDegradedRegistry})
	return publicLinkRules(out), linkRulesRefusal(err, "DeleteProjectLinkRules projectRef")
}
func linkRulesRefusal(err error, field string) error {
	if err == nil {
		return nil
	}
	var refusal *service.Refusal
	if errors.As(err, &refusal) {
		message := refusal.Error()
		if refusal.Code == service.CodeConfirmationRequired {
			message = "DeleteProjectLinkRules confirm must be true; nothing was deleted"
		}
		return &Refusal{Code: Code(refusal.Code), Field: refusal.Field, Reason: refusal.Reason, message: message}
	}
	var project *service.ProjectError
	if errors.As(err, &project) {
		err := selectorInputError(project.Err, field)
		var input *selector.SelectorError
		if errors.As(err, &input) && strings.HasPrefix(input.Detail, "--project ") {
			rendered := *input
			rendered.Detail = field + strings.TrimPrefix(input.Detail, "--project")
			err = &rendered
		}
		return resourceRefusal(err)
	}
	return err
}
func publicLinkRules(out service.Result) core.ProjectLinkRulesResult {
	result := core.ProjectLinkRulesResult{Project: out.Project, Jira: out.Jira, Repo: out.Repo, URLs: make([]core.ProjectLinkNamedURL, len(out.URLs)), Links: make([]core.ProjectLinkExample, len(out.Links)), ExampleValue: out.ExampleValue, Placeholders: out.Placeholders, ProjectVariables: make([]core.ProjectLinkVariable, len(out.ProjectVariables)), DryRun: out.DryRun}
	for i, v := range out.URLs {
		result.URLs[i] = core.ProjectLinkNamedURL(v)
	}
	for i, v := range out.Links {
		result.Links[i] = core.ProjectLinkExample(v)
	}
	for i, v := range out.ProjectVariables {
		result.ProjectVariables[i] = core.ProjectLinkVariable(v)
	}
	return result
}
