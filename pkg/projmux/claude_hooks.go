package projmux

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/crevissepartners/projmux/internal/services/claudesettings"
	"github.com/crevissepartners/projmux/internal/services/hookcatalog"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

const (
	// CodeClaudeSettingsChanged refuses an apply whose Base is not the
	// settings file's current content, or whose file changed while it wrote.
	CodeClaudeSettingsChanged Code = claudesettings.CodeChanged
	// CodeClaudeHooksConflict refuses an apply the integration itself refuses;
	// the plan's Conflict says why.
	CodeClaudeHooksConflict Code = "claude-hooks-conflict"
	// CodeClaudeSettingsUnreadable refuses a settings file that cannot be read
	// or parsed.
	CodeClaudeSettingsUnreadable Code = "claude-settings-unreadable"
)

// claudeHooksApply serializes the applies of one process: an apply holds its
// one slot from planning to the end of its write.
var claudeHooksApply = make(chan struct{}, 1)

// PreviewClaudeHooks plans `projmux agent integrate claude` (with --remove
// when request.Remove) for the settings file under the Client's HomeDir and
// writes nothing. The installed events come from the embedded Claude hook
// catalog merged with the override under the Client's ConfigDir. A settings
// file that cannot be read or parsed is a CodeClaudeSettingsUnreadable
// refusal; a conflict is not a refusal, it is the plan's Conflict. A context
// canceled before or while the file is read returns its error and no plan.
func (c *Client) PreviewClaudeHooks(ctx context.Context, request core.ClaudeHooksRequest) (core.ClaudeHooksPlan, error) {
	if err := ctx.Err(); err != nil {
		return core.ClaudeHooksPlan{}, err
	}
	plan, _, err := c.planClaudeHooks(request.Remove)
	if err != nil {
		return core.ClaudeHooksPlan{}, err
	}
	if err := ctx.Err(); err != nil {
		return core.ClaudeHooksPlan{}, err
	}
	return plan, nil
}

// ApplyClaudeHooks plans again as PreviewClaudeHooks does and carries the plan
// out as `projmux agent integrate claude` does. It is refused with
// CodeClaudeSettingsChanged when request.Base is not the current file's Base,
// with CodeClaudeHooksConflict when the plan has a Conflict, and with
// CodeClaudeSettingsUnreadable when the file cannot be read or parsed. A plan
// with nothing to change writes nothing. Otherwise the file is replaced
// atomically, keeping its mode and a symbolic link's target, and the replace
// is refused with CodeClaudeSettingsChanged if the file changed after it was
// read. Applies are serialized within one process only. A context canceled
// before the write starts, including while waiting for another apply, returns
// its error and writes nothing; a write already started completes.
func (c *Client) ApplyClaudeHooks(ctx context.Context, request core.ApplyClaudeHooksRequest) (core.ApplyClaudeHooksResult, error) {
	if err := ctx.Err(); err != nil {
		return core.ApplyClaudeHooksResult{}, err
	}
	select {
	case claudeHooksApply <- struct{}{}:
	case <-ctx.Done():
		return core.ApplyClaudeHooksResult{}, ctx.Err()
	}
	defer func() { <-claudeHooksApply }()
	if err := ctx.Err(); err != nil {
		return core.ApplyClaudeHooksResult{}, err
	}
	plan, base, err := c.planClaudeHooks(request.Remove)
	if err != nil {
		return core.ApplyClaudeHooksResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return core.ApplyClaudeHooksResult{}, err
	}
	if request.Base != plan.Base {
		return core.ApplyClaudeHooksResult{}, &Refusal{Code: CodeClaudeSettingsChanged, message: (&claudesettings.ChangedError{Path: plan.Path}).Error()}
	}
	if plan.Conflict != "" {
		return core.ApplyClaudeHooksResult{}, &Refusal{Code: CodeClaudeHooksConflict, message: plan.Conflict}
	}
	result := core.ApplyClaudeHooksResult{Path: plan.Path, Action: plan.Action}
	if !plan.Changed {
		return result, nil
	}
	if err := ctx.Err(); err != nil {
		return core.ApplyClaudeHooksResult{}, err
	}
	if err := claudesettings.Replace(plan.Path, base, []byte(plan.Next)); err != nil {
		var changed *claudesettings.ChangedError
		if errors.As(err, &changed) {
			return core.ApplyClaudeHooksResult{}, &Refusal{Code: CodeClaudeSettingsChanged, message: err.Error(), cause: err}
		}
		return core.ApplyClaudeHooksResult{}, fmt.Errorf("write Claude settings %s: %w", plan.Path, err)
	}
	result.Changed = true
	return result, nil
}

// planClaudeHooks reads the settings file and plans it as the CLI does,
// returning the bytes it read as the base a write must still find.
func (c *Client) planClaudeHooks(remove bool) (core.ClaudeHooksPlan, *claudesettings.Base, error) {
	if c.discoveryHome == "" {
		return core.ClaudeHooksPlan{}, nil, &Refusal{Code: CodeClaudeSettingsUnreadable, message: "resolve home directory: no home directory"}
	}
	path := filepath.Join(c.discoveryHome, claudesettings.SettingsRelativePath)
	current, exists, err := claudesettings.ReadState(path, os.ReadFile)
	if err != nil {
		return core.ClaudeHooksPlan{}, nil, &Refusal{Code: CodeClaudeSettingsUnreadable, message: err.Error(), cause: err}
	}
	var catalogErr error
	plan, err := claudesettings.PlanHooks(remove, !remove, path, current, func() ([]string, error) {
		events, err := c.claudeHookInstallEvents()
		catalogErr = err
		return events, err
	})
	if err != nil {
		if catalogErr != nil {
			return core.ClaudeHooksPlan{}, nil, err
		}
		return core.ClaudeHooksPlan{}, nil, &Refusal{Code: CodeClaudeSettingsUnreadable, message: err.Error(), cause: err}
	}
	out := core.ClaudeHooksPlan{Path: plan.Path, Exists: exists, Changed: plan.Changed, Current: plan.Current, Next: plan.Next, Conflict: plan.Conflict, Action: plan.Action}
	if exists {
		sum := sha256.Sum256([]byte(current))
		out.Base = hex.EncodeToString(sum[:])
	}
	return out, &claudesettings.Base{Exists: exists, Data: []byte(current)}, nil
}

// claudeHookInstallEvents is the Claude events the integration installs: the
// embedded catalog merged with the override in the Client's ConfigDir.
func (c *Client) claudeHookInstallEvents() ([]string, error) {
	catalog, err := hookcatalog.Default(hookcatalog.ProviderClaude)
	if err != nil {
		return nil, err
	}
	if c.paths.ConfigDir != "" {
		if catalog, err = hookcatalog.ApplyOverride(catalog, c.paths.AIHookCatalogOverrideFile(hookcatalog.ProviderClaude), os.ReadFile); err != nil {
			return nil, err
		}
	}
	return hookcatalog.InstallEvents(catalog), nil
}
