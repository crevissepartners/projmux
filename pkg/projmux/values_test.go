package projmux_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestStatusbarValuesEqualInternal(t *testing.T) {
	if string(core.StatusbarVisibilityOn) != string(config.StatusbarVisibilityOn) ||
		string(core.StatusbarVisibilityOff) != string(config.StatusbarVisibilityOff) {
		t.Fatalf("visibility on/off %q %q", core.StatusbarVisibilityOn, core.StatusbarVisibilityOff)
	}
	for public, internal := range map[core.StatusbarVisibilitySource]config.StatusbarVisibilitySource{
		core.StatusbarVisibilitySourceDefault: config.StatusbarVisibilitySourceDefault,
		core.StatusbarVisibilitySourceSaved:   config.StatusbarVisibilitySourceSaved,
		core.StatusbarVisibilitySourceCentral: config.StatusbarVisibilitySourceCentral,
	} {
		if string(public) != string(internal) {
			t.Fatalf("source %q != %q", public, internal)
		}
	}
	got, want := core.DefaultStatusbarVisibilityState(), config.DefaultStatusbarVisibilityState()
	if string(got.Effective) != string(want.Effective) || got.Saved != want.Saved || string(got.Source) != string(want.Source) || got.Invalid != want.Invalid {
		t.Fatalf("default state %+v != %+v", got, want)
	}
}

func TestStatusbarValuesMatchConfigReads(t *testing.T) {
	client, err := projmux.NewClient(projmux.Options{HomeDir: t.TempDir(), ConfigDir: t.TempDir(), StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range client.GetCentralStatusbarDefaults() {
		if core.StatusbarVisibility(state.Effective) != core.StatusbarVisibilityOn && core.StatusbarVisibility(state.Effective) != core.StatusbarVisibilityOff {
			t.Fatalf("%s effective %q is neither public value", state.Key, state.Effective)
		}
		if core.StatusbarVisibilitySource(state.Source) != core.StatusbarVisibilitySourceDefault {
			t.Fatalf("%s source %q on an empty ConfigDir", state.Key, state.Source)
		}
	}
}

func TestAgentQuestionWindowUnlimitedEqualsInternal(t *testing.T) {
	if core.AgentQuestionWindowUnlimitedWord != config.AgentQuestionWindowUnlimitedWord {
		t.Fatalf("word %q != %q", core.AgentQuestionWindowUnlimitedWord, config.AgentQuestionWindowUnlimitedWord)
	}
	if core.AgentQuestionWindowUnlimitedSeconds != config.UnlimitedAgentQuestionWindowSeconds {
		t.Fatalf("seconds %d != %d", core.AgentQuestionWindowUnlimitedSeconds, config.UnlimitedAgentQuestionWindowSeconds)
	}
	path := filepath.Join(t.TempDir(), "window")
	if err := os.WriteFile(path, []byte(core.AgentQuestionWindowUnlimitedWord+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seconds, err := config.LoadAgentQuestionWindowSecondsFile(path)
	if err != nil || seconds != core.AgentQuestionWindowUnlimitedSeconds {
		t.Fatalf("word reads as %d %v", seconds, err)
	}
}

func TestCodexThreadPolicyMismatchIdentity(t *testing.T) {
	if projmux.CodexThreadPolicyMismatchReason != codexappserver.ReasonPolicyMismatch {
		t.Fatalf("reason %q != %q", projmux.CodexThreadPolicyMismatchReason, codexappserver.ReasonPolicyMismatch)
	}
	mismatch := &codexappserver.PolicyMismatchError{Method: "thread/start"}
	for name, err := range map[string]error{
		"bare sentinel": projmux.ErrCodexThreadPolicyMismatch,
		"typed":         mismatch,
		"wrapped":       fmt.Errorf("resume: %w", mismatch),
		"refusal":       fmt.Errorf("relaunch: %w", errors.Join(errors.New("other"), mismatch)),
	} {
		if !errors.Is(err, projmux.ErrCodexThreadPolicyMismatch) || !errors.Is(err, codexappserver.ErrPolicyMismatch) {
			t.Fatalf("%s: errors.Is false", name)
		}
	}
	for name, err := range map[string]error{
		"nil":      nil,
		"plain":    errors.New("codex app-server thread policy mismatch"),
		"settings": codexappserver.ErrSettingsNotApplied,
	} {
		if errors.Is(err, projmux.ErrCodexThreadPolicyMismatch) {
			t.Fatalf("%s: errors.Is true", name)
		}
	}
}

func TestIsUsageErrorEqualsInternal(t *testing.T) {
	usage := &metadata.InputError{Op: "op", Detail: "bad input", Cause: errors.New("cause")}
	for name, err := range map[string]error{
		"nil":            nil,
		"usage":          usage,
		"wrapped usage":  fmt.Errorf("outer: %w", usage),
		"joined usage":   errors.Join(errors.New("other"), usage),
		"plain":          errors.New("plain"),
		"state":          &metadata.StateError{Op: "op", Cause: errors.New("cause")},
		"wrapped plain":  fmt.Errorf("outer: %w", errors.New("plain")),
		"refusal usage":  &projmux.Refusal{Code: projmux.CodeCreateRequestInvalid},
		"wrapped refuse": fmt.Errorf("outer: %w", &projmux.Refusal{Code: projmux.CodeCreateRequestInvalid}),
	} {
		if got, want := projmux.IsUsageError(err), metadata.IsUsageError(err); got != want {
			t.Fatalf("%s: public %v internal %v", name, got, want)
		}
	}
	if !projmux.IsUsageError(fmt.Errorf("outer: %w", usage)) || projmux.IsUsageError(nil) || projmux.IsUsageError(errors.New("plain")) {
		t.Fatal("representative table is not the expected judgement")
	}
}
