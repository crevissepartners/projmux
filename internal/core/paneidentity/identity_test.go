package paneidentity

import "testing"

func TestResolveVisiblePaneIdentityMatrix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   Inputs
		want Identity
	}{
		{name: "label owns visible identity", in: Inputs{Label: " user label ", AIAgent: "codex", AITopic: "AI topic", Command: "zsh", Title: "raw title"}, want: Identity{Value: "user label", Source: SourceLabel}},
		{name: "agent topic follows label", in: Inputs{AIAgent: "codex", AITopic: " AI topic ", Command: "zsh", Title: "raw title"}, want: Identity{Value: "AI topic", Source: SourceTopic}},
		{name: "topic without agent is not visible topic", in: Inputs{AITopic: "orphan topic", Command: "zsh", Title: "raw title"}, want: Identity{Value: "zsh", Source: SourceShell}},
		{name: "known shell follows topic", in: Inputs{Command: "fish", Title: "branch title"}, want: Identity{Value: "fish", Source: SourceShell}},
		{name: "unknown command does not replace raw title", in: Inputs{Command: "nvim", Title: "raw title"}, want: Identity{Value: "raw title", Source: SourceTitle}},
		{name: "raw title is final fallback", in: Inputs{Title: " raw title "}, want: Identity{Value: "raw title", Source: SourceTitle}},
		{name: "all absent", in: Inputs{Command: "nvim"}, want: Identity{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Resolve(tt.in); got != tt.want {
				t.Fatalf("Resolve(%#v) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}
