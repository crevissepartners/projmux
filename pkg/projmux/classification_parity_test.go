package projmux_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	message "github.com/crevissepartners/projmux/internal/core/agentmessage"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/core/profile"
	"github.com/crevissepartners/projmux/pkg/projmux"
)

// The public classification values are the internal ones, so a consumer that
// switches to them keeps the same decisions.
func TestPublicClassificationParity(t *testing.T) {
	for _, tc := range []struct {
		name         string
		public, core string
	}{
		{"persona provider unsupported", string(projmux.CodePersonaProviderUnsupported), persona.ReasonProviderUnsupported},
		{"persona unavailable", string(projmux.CodePersonaUnavailable), persona.ReasonUnavailable},
		{"operator origin target not claude", string(projmux.CodeOperatorOriginTargetNotClaude), message.ReasonOperatorOriginTargetNotClaude},
		{"reserved profile name", projmux.ReservedProfileName, profile.ReservedName},
	} {
		if tc.public != tc.core || tc.public == "" {
			t.Fatalf("%s: public %q, internal %q", tc.name, tc.public, tc.core)
		}
	}
	if projmux.CodeOperatorOriginTargetNotClaude != "operator-origin-target-not-claude" || projmux.ReservedProfileName != "none" {
		t.Fatal("stable values changed")
	}
	if projmux.CodePersonaProviderUnsupported == projmux.CodePersonaUnavailable {
		t.Fatal("persona codes do not distinguish their refusals")
	}
	var refusal *projmux.Refusal
	if err := projmux.ValidateProfileName(projmux.ReservedProfileName); !errors.As(err, &refusal) || refusal.Code != projmux.CodeProfileNameReserved {
		t.Fatalf("reserved name: %v", err)
	}
}

// The public sentinel is the internal error value, so errors.Is decides the
// same way on every wrapping the existing refusal path produces.
func TestErrOperatorOriginTargetNotClaudeParity(t *testing.T) {
	if !errors.Is(projmux.ErrOperatorOriginTargetNotClaude, message.ErrOperatorOriginTargetNotClaude) || !errors.Is(message.ErrOperatorOriginTargetNotClaude, projmux.ErrOperatorOriginTargetNotClaude) {
		t.Fatal("public sentinel differs from the internal error")
	}
	for _, err := range []error{
		message.ErrOperatorOriginTargetNotClaude,
		fmt.Errorf("send: %w", message.ErrOperatorOriginTargetNotClaude),
		fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", message.ErrOperatorOriginTargetNotClaude)),
	} {
		if errors.Is(err, projmux.ErrOperatorOriginTargetNotClaude) != errors.Is(err, message.ErrOperatorOriginTargetNotClaude) || !errors.Is(err, projmux.ErrOperatorOriginTargetNotClaude) {
			t.Fatalf("errors.Is differs for %v", err)
		}
		if !errors.Is(err, message.ErrInvalidEnvelope) {
			t.Fatalf("lost the envelope refusal class: %v", err)
		}
	}
	for _, err := range []error{message.ErrOperatorOriginReply, message.ErrInvalidEnvelope, errors.New(message.ReasonOperatorOriginTargetNotClaude)} {
		if errors.Is(err, projmux.ErrOperatorOriginTargetNotClaude) {
			t.Fatalf("matched another refusal: %v", err)
		}
	}
}

func TestNormalizeProviderParity(t *testing.T) {
	inputs := []string{"", " ", "codex", "Codex", " CLAUDE ", "claude\n", "antigravity", "Antigravity", "gemini", "openai", "claude-code", "codex claude", "\tcodex\t"}
	for _, meta := range aiprovider.All() {
		inputs = append(inputs, string(meta.ID), " "+string(meta.ID)+" ")
	}
	for _, in := range inputs {
		if got, want := projmux.NormalizeProvider(in), metadata.NormalizeProvider(in); got != want {
			t.Fatalf("NormalizeProvider(%q) = %q, internal %q", in, got, want)
		}
	}
	for in, want := range map[string]string{" Claude ": "claude", "CODEX": "codex", "unknown": "", "": ""} {
		if got := projmux.NormalizeProvider(in); got != want {
			t.Fatalf("NormalizeProvider(%q) = %q, want %q", in, got, want)
		}
	}
}
