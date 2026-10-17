package projmux

import (
	"context"
	"errors"
	"strings"

	"github.com/crevissepartners/projmux/internal/services/settings"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

const (
	CodeConfigKeyInvalid   Code = "config-key-invalid"
	CodeConfigValueInvalid Code = "config-value-invalid"
)

// GetConfig reads one public central setting. Providers and policies retain
// the CLI's default-on-read-error behavior; locale parse errors are returned.
func (c *Client) GetConfig(ctx context.Context, key core.ConfigKey) (core.ConfigValue, error) {
	v, err := (settings.Service{Paths: c.instructions.Paths}).Get(ctx, string(key))
	if err != nil {
		return core.ConfigValue{}, settingsRefusal(err)
	}
	return publicConfig(v), nil
}

// SetConfig writes the same files as config providers/locale/agent-questions/
// agent-approvals/agent-host and the central resources, mode and visibility routes.
// Cancellation before admission writes nothing. Policy writes
// finish once admitted; the two policy files are not a multi-file transaction.
func (c *Client) SetConfig(ctx context.Context, u core.ConfigUpdate) (core.ConfigValue, error) {
	v, err := (settings.Service{Paths: c.instructions.Paths}).Set(ctx, settings.Update{Key: string(u.Key), Provider: u.Provider, Enabled: u.Enabled, Locale: u.Locale, Answering: u.Answering, Window: u.Window, DefaultHost: u.DefaultHost, Resources: u.Resources, DefaultMode: u.DefaultMode, Visibility: u.Visibility})
	if err != nil {
		return core.ConfigValue{}, settingsRefusal(err)
	}
	return publicConfig(v), nil
}
func publicConfig(v settings.Value) core.ConfigValue {
	out := core.ConfigValue{Key: core.ConfigKey(v.Key), Locale: v.Locale, Source: v.Source, Answering: v.Answering, Window: v.Window, DefaultHost: v.DefaultHost}
	if v.Resources != nil {
		out.Resources = &core.ConfigToggleState{Effective: v.Resources.Effective, Saved: v.Resources.Saved, Source: v.Resources.Source, Invalid: v.Resources.Invalid}
	}
	if v.Visibility != nil {
		out.Visibility = make([]core.ConfigVisibilityState, len(v.Visibility))
		for i, v := range v.Visibility {
			out.Visibility[i] = core.ConfigVisibilityState(v)
		}
	}
	out.DefaultMode = v.DefaultMode
	if v.Providers != nil {
		out.Providers = make([]core.ProviderState, len(v.Providers))
		for i, p := range v.Providers {
			out.Providers[i] = core.ProviderState(p)
		}
	}
	return out
}
func settingsRefusal(err error) error {
	var input *settings.InputError
	if errors.As(err, &input) {
		return &Refusal{Code: Code(input.Code), message: settingsInputMessage(err.Error())}
	}
	return err
}

// Translate only generated input prefixes; quoted caller values stay intact.
func settingsInputMessage(message string) string {
	for _, p := range []struct{ cli, sdk string }{
		{"config resources --set", "ConfigUpdate.Resources"},
		{"config ai-default-mode --set", "ConfigUpdate.DefaultMode"},
		{"config statusbar-visibility --set", "ConfigUpdate.Visibility"},
		{"config agent-host --default", "ConfigUpdate.DefaultHost"},
		{"config providers --enable", "ConfigUpdate.Provider"},
		{"config providers --disable", "ConfigUpdate.Provider"},
		{"config locale --set", "ConfigUpdate.Locale"},
		{"config agent-questions --answering", "ConfigUpdate.Answering"},
		{"config agent-approvals --answering", "ConfigUpdate.Answering"},
		{"config agent-questions --window", "ConfigUpdate.Window"},
		{"config agent-approvals --window", "ConfigUpdate.Window"},
	} {
		if detail, ok := strings.CutPrefix(message, p.cli); ok {
			return p.sdk + detail
		}
	}
	return message
}
