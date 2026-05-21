package app

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// trust.go implements the Settings popup "Trust" badge + subsection used
// by the Project tab. The Project tab's first picker row renders the
// current trust standing of the project-local .projmux/config.toml file:
//
//   - absent    → no .projmux/config.toml on disk; trust is moot.
//   - untrusted → file exists, no trust-store entry yet.
//   - trusted   → trust-store hash matches the file on disk.
//   - stale     → trust-store entry exists but the file hash changed.
//
// Enter on the Trust row opens this subsection so the user can confirm
// the side-effecting action (trust / refresh / untrust). The subsection
// reuses the standard picker chrome so it inherits the same back/close
// behaviour as the other Project tab pages. The Global tab is exempt
// from the Trust surface because the trust policy only governs
// project-local configs — see hooks.InspectProjectConfigTrust for the
// state machine.

// projectTrustEntry builds the first row of the Project tab. It surfaces
// the current trust standing and routes Enter into the Trust subsection
// which decides whether to register, refresh, or untrust the project
// config.
func (c *settingsCommand) projectTrustEntry(ctx settingsProjectContext) intpickercompat.Entry {
	report, err := c.inspectProjectTrust(ctx)
	if err != nil {
		// A read-only failure (e.g. the trust store is unreadable) keeps
		// the badge dim so the rest of the Project tab stays usable.
		// We still route Enter into the subsection so the user sees the
		// error message in a dedicated page instead of losing it on the
		// background log.
		return intpickercompat.Entry{
			Label: settingsLabelDim("Trust", "trust state unavailable - "+err.Error()),
			Value: settingsSectionProjectTrust,
		}
	}
	glyph, color, summary := trustBadgeAppearance(report.State)
	return intpickercompat.Entry{
		Label: settingsLabel(glyph, color, "Trust", summary),
		Value: settingsSectionProjectTrust,
	}
}

// trustBadgeAppearance maps a trust state to the picker row glyph,
// colour, and dim summary used by the Project tab badge. The glyphs
// reuse the existing settings palette so the Trust row reads as part of
// the same design system as the other rows.
func trustBadgeAppearance(state hooks.ProjectConfigTrustState) (string, string, string) {
	switch state {
	case hooks.ProjectConfigTrustTrusted:
		return settingsGlyphToggle, settingsColorTrustTrusted, "trusted - hash matches stored entry"
	case hooks.ProjectConfigTrustStale:
		return settingsGlyphInactive, settingsColorTrustStale, "stale - file changed since trust"
	case hooks.ProjectConfigTrustUntrusted:
		return settingsGlyphInfo, settingsColorTrustUntrusted, "untrusted - registration required"
	case hooks.ProjectConfigTrustAbsent:
		return settingsGlyphInfo, settingsColorDim, "no .projmux/config.toml on disk"
	default:
		return settingsGlyphInfo, settingsColorDim, "unknown trust state"
	}
}

// Trust states use their own small palette so stale/untrusted/trusted rows do
// not borrow warning, danger, action, or muted tones.
const (
	settingsColorTrustTrusted   = "\x1b[38;2;154;191;136m"
	settingsColorTrustStale     = "\x1b[38;2;177;139;212m"
	settingsColorTrustUntrusted = "\x1b[38;2;210;139;88m"
)

// inspectProjectTrust resolves the trust store path and asks the hooks
// package to inspect the project config trust standing.
func (c *settingsCommand) inspectProjectTrust(ctx settingsProjectContext) (hooks.ProjectConfigTrustReport, error) {
	if !ctx.hasProject() {
		return hooks.ProjectConfigTrustReport{}, errors.New("project context is required")
	}
	trustPath, err := c.projectConfigTrustStorePath()
	if err != nil {
		return hooks.ProjectConfigTrustReport{}, err
	}
	return hooks.InspectProjectConfigTrust(ctx.Path, trustPath)
}

// runProjectTrustSection drives the Trust subsection picker. The page
// shows the project path, the resolved config path, the current trust
// state, and one actionable row whose meaning depends on the state. A
// destructive Untrust action goes through an extra confirmation page so
// the user does not accidentally invalidate trust with a single Enter.
func (c *settingsCommand) runProjectTrustSection(stdout, stderr io.Writer) error {
	for {
		ctx := c.resolveSettingsProjectContext()
		options := c.projectTrustOptions(ctx)
		result, err := c.runPicker(options)
		if err != nil {
			return err
		}
		action := strings.TrimSpace(result.Value)
		if result.Key != "enter" || action == "" {
			return errSettingsClosed
		}
		switch action {
		case settingsBackValue:
			return nil
		case settingsNoopValue:
			continue
		case settingsTrustApply:
			if err := c.runProjectTrustApply(ctx, stdout, stderr); err != nil {
				return err
			}
		case settingsTrustRefresh:
			// Refresh is the same call as apply — TrustProjectConfig is
			// idempotent and just rewrites the stored hash so the runner
			// accepts the updated file on its next invocation.
			if err := c.runProjectTrustApply(ctx, stdout, stderr); err != nil {
				return err
			}
		case settingsTrustUntrust:
			confirmed, err := c.confirmProjectTrustUntrust(ctx)
			if err != nil {
				return err
			}
			if !confirmed {
				continue
			}
			if err := c.runProjectTrustUntrust(ctx, stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown trust action: %s", action)
		}
	}
}

// projectTrustOptions builds the Trust subsection picker. Layout mirrors
// the Effective merge view page so the two project-scope pages feel
// consistent (info rows + a small set of actionable rows).
func (c *settingsCommand) projectTrustOptions(ctx settingsProjectContext) intpickercompat.Options {
	return intpickercompat.Options{
		UI:         "settings-project-trust",
		Entries:    c.projectTrustEntries(ctx),
		Title:      "Trust - Project config hash",
		Prompt:     "Settings > Project > Trust > ",
		Footer:     projmuxFooter("Enter: apply  |  Back row: parent "),
		ExpectKeys: []string{"enter"},
		Bindings:   settingsCloseBindings(),
	}
}

func (c *settingsCommand) projectTrustEntries(ctx settingsProjectContext) []intpickercompat.Entry {
	entries := []intpickercompat.Entry{settingsBackEntry()}
	if !ctx.hasProject() {
		return append(entries, intpickercompat.Entry{
			Label: settingsLabelDim("Trust", "disabled - no project context"),
			Value: settingsNoopValue,
		})
	}

	configPath := settingsProjectConfigPath(ctx)
	entries = append(entries,
		intpickercompat.Entry{
			Label: settingsLabelInfo("Project context", ctx.Path, ctx.Source),
			Value: settingsNoopValue,
		},
		intpickercompat.Entry{
			Label: settingsLabelInfo("Project config", configPath, "trust target"),
			Value: settingsNoopValue,
		},
	)

	report, err := c.inspectProjectTrust(ctx)
	if err != nil {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelDim("Trust state error", err.Error()),
			Value: settingsNoopValue,
		})
		return entries
	}

	glyph, color, summary := trustBadgeAppearance(report.State)
	entries = append(entries, intpickercompat.Entry{
		Label: settingsLabel(glyph, color, "State", summary),
		Value: settingsNoopValue,
	})

	if report.StoredHash != "" {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfo("Stored hash", report.StoredHash, "trust-store"),
			Value: settingsNoopValue,
		})
	}
	if report.CurrentHash != "" {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfo("Current hash", report.CurrentHash, "on-disk"),
			Value: settingsNoopValue,
		})
	}

	switch report.State {
	case hooks.ProjectConfigTrustUntrusted:
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabel(settingsGlyphAdd, settingsColorAdd, "Trust this config", "record current hash"),
			Value: settingsTrustApply,
		})
	case hooks.ProjectConfigTrustStale:
		entries = append(entries,
			intpickercompat.Entry{
				Label: settingsLabel(settingsGlyphType, settingsColorType, "Refresh trust", "rewrite stored hash to match current file"),
				Value: settingsTrustRefresh,
			},
			intpickercompat.Entry{
				Label: settingsLabel(settingsGlyphRemove, settingsColorRemove, "Untrust", "remove trust-store entry"),
				Value: settingsTrustUntrust,
			},
		)
	case hooks.ProjectConfigTrustTrusted:
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabel(settingsGlyphRemove, settingsColorRemove, "Untrust", "remove trust-store entry"),
			Value: settingsTrustUntrust,
		})
	case hooks.ProjectConfigTrustAbsent:
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelDim("No actions", "create "+filepath.Base(configPath)+" first via Project recipe row"),
			Value: settingsNoopValue,
		})
	}
	return entries
}

func (c *settingsCommand) runProjectTrustApply(ctx settingsProjectContext, stdout, stderr io.Writer) error {
	if !ctx.hasProject() {
		return errors.New("trust requires a project context")
	}
	trustPath, err := c.projectConfigTrustStorePath()
	if err != nil {
		return err
	}
	sum, err := hooks.TrustProjectConfig(ctx.Path, trustPath)
	if err != nil {
		fmt.Fprintf(stderr, "trust failed: %v\n", err)
		return nil
	}
	configPath := settingsProjectConfigPath(ctx)
	_, _ = fmt.Fprintf(stdout, "trusted %s\n", configPath)
	_, _ = fmt.Fprintf(stdout, "sha256 %s\n", sum)
	return nil
}

func (c *settingsCommand) runProjectTrustUntrust(ctx settingsProjectContext, stdout, stderr io.Writer) error {
	if !ctx.hasProject() {
		return errors.New("untrust requires a project context")
	}
	trustPath, err := c.projectConfigTrustStorePath()
	if err != nil {
		return err
	}
	if _, err := hooks.UntrustProjectConfig(ctx.Path, trustPath); err != nil {
		fmt.Fprintf(stderr, "untrust failed: %v\n", err)
		return nil
	}
	configPath := settingsProjectConfigPath(ctx)
	_, _ = fmt.Fprintf(stdout, "untrusted %s\n", configPath)
	return nil
}

// confirmProjectTrustUntrust shows a dedicated Yes/No picker so an
// accidental Enter on the destructive Untrust row does not silently
// invalidate the trust-store entry. Cancel (Esc/close) is treated the
// same as No so the popup-toggle invariant is preserved.
func (c *settingsCommand) confirmProjectTrustUntrust(ctx settingsProjectContext) (bool, error) {
	configPath := settingsProjectConfigPath(ctx)
	options := intpickercompat.Options{
		UI: "settings-project-trust-confirm",
		Entries: []intpickercompat.Entry{
			{
				Label: settingsLabelInfo("Untrust", configPath, "destructive"),
				Value: settingsNoopValue,
			},
			{
				Label: settingsLabel(settingsGlyphBack, settingsColorBack, "Cancel", "keep current trust"),
				Value: settingsTrustConfirmNo,
			},
			{
				Label: settingsLabel(settingsGlyphRemove, settingsColorRemove, "Yes, untrust", "remove trust-store entry"),
				Value: settingsTrustConfirmYes,
			},
		},
		Title:      "Untrust project config - confirm",
		Prompt:     "Settings > Project > Trust > Untrust > ",
		Footer:     projmuxFooter("Enter: confirm "),
		ExpectKeys: []string{"enter"},
		Bindings:   settingsCloseBindings(),
	}
	result, err := c.runPicker(options)
	if err != nil {
		if errors.Is(err, errSettingsClosed) {
			return false, nil
		}
		return false, err
	}
	value := strings.TrimSpace(result.Value)
	if result.Key != "enter" || value == "" {
		return false, nil
	}
	return value == settingsTrustConfirmYes, nil
}

// Trust subsection action values. Each one lives in the trust: prefix
// namespace so settingsEntryMetaForValue can resolve their telemetry meta
// through the catalog rather than hard-coding a per-row entry.
const (
	settingsTrustApply      = "trust:apply"
	settingsTrustRefresh    = "trust:refresh"
	settingsTrustUntrust    = "trust:untrust"
	settingsTrustConfirmYes = "trust:confirm-yes"
	settingsTrustConfirmNo  = "trust:confirm-no"
)
