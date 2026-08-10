package app

import (
	"testing"

	intpsmux "github.com/crevissepartners/projmux/internal/integrations/psmux"
)

func TestSelectedMuxBackendUsesPlatformDefault(t *testing.T) {
	t.Parallel()

	if got := selectedMuxBackend(func(string) string { return "" }, func() string { return "windows" }); got != muxBackendPSMux {
		t.Fatalf("windows default backend = %q, want %q", got, muxBackendPSMux)
	}
	if got := selectedMuxBackend(func(string) string { return "" }, func() string { return "linux" }); got != muxBackendTmux {
		t.Fatalf("linux default backend = %q, want %q", got, muxBackendTmux)
	}
}

func TestSelectedMuxBackendEnvOverridesPlatform(t *testing.T) {
	t.Parallel()

	if got := selectedMuxBackend(func(name string) string {
		if name == muxBackendEnvVar {
			return "psmux"
		}
		return ""
	}, func() string { return "linux" }); got != muxBackendPSMux {
		t.Fatalf("env psmux backend = %q, want %q", got, muxBackendPSMux)
	}
	if got := selectedMuxBackend(func(name string) string {
		if name == muxBackendEnvVar {
			return "tmux"
		}
		return ""
	}, func() string { return "windows" }); got != muxBackendTmux {
		t.Fatalf("env tmux backend = %q, want %q", got, muxBackendTmux)
	}
}

func TestDefaultAppSessionCommandsUsePSMuxClientWhenSelected(t *testing.T) {
	t.Setenv(muxBackendEnvVar, "psmux")

	switcher := newSwitchCommand()
	if _, ok := switcher.sessions.(*intpsmux.Client); !ok {
		t.Fatalf("switch sessions = %T, want *psmux.Client", switcher.sessions)
	}
	if inv, ok := switcher.inventory.(tmuxPreviewInventory); !ok {
		t.Fatalf("switch inventory = %T, want tmuxPreviewInventory adapter", switcher.inventory)
	} else if _, ok := inv.client.(*intpsmux.Client); !ok {
		t.Fatalf("switch inventory client = %T, want *psmux.Client", inv.client)
	}

	attach := newAttachCommand()
	if _, ok := attach.sessions.(*intpsmux.Client); !ok {
		t.Fatalf("attach sessions = %T, want *psmux.Client", attach.sessions)
	}

	sessions := newSessionsCommand()
	if _, ok := sessions.recent.(*intpsmux.Client); !ok {
		t.Fatalf("sessions recent = %T, want *psmux.Client", sessions.recent)
	}
}
