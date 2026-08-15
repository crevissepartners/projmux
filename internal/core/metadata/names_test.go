package metadata

import (
	"errors"
	"testing"
)

func TestNameBasesFollowTheDeclaredSeedPriorityAndExcludeTopicsAndTitles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		got  string
		want string
	}{
		{name: "window prefers explicit name", got: WindowNameBase("build", "nvim .", "/bin/zsh"), want: "build"},
		{name: "window falls back to initial command basename", got: WindowNameBase("", "/usr/bin/nvim .", "/bin/zsh"), want: "nvim"},
		{name: "window falls back to configured shell basename", got: WindowNameBase("", "", "/bin/zsh"), want: "zsh"},
		{name: "window falls back to the window literal", got: WindowNameBase("", "", ""), want: "window"},
		{name: "shell pane prefers command basename", got: PaneNameBase("/usr/bin/htop", "/bin/zsh"), want: "htop"},
		{name: "shell pane falls back to configured shell basename", got: PaneNameBase("", "/usr/local/bin/fish"), want: "fish"},
		{name: "shell pane falls back to the pane literal", got: PaneNameBase("", ""), want: "pane"},
		{name: "managed pane uses the agent name base", got: ManagedPaneNameBase("codex-1"), want: "codex-1-pane"},
		{name: "managed pane falls back to the pane literal", got: ManagedPaneNameBase(""), want: "pane"},
		{name: "agent prefers explicit name", got: AgentNameBase("reviewer", "codex"), want: "reviewer"},
		{name: "agent normalizes a known provider", got: AgentNameBase("", "Codex"), want: "codex"},
		{name: "agent normalizes claude", got: AgentNameBase("", "claude"), want: "claude"},
		{name: "agent normalizes antigravity", got: AgentNameBase("", "antigravity"), want: "antigravity"},
		{name: "agent falls back for an unknown provider", got: AgentNameBase("", "mystery"), want: "agent"},
		{name: "project seeds from the root basename", got: ProjectNameBase("/home/user/src/Projmux"), want: "Projmux"},
		{name: "project falls back for the filesystem root", got: ProjectNameBase("/"), want: "project"},
		{name: "project display name seeds from the root basename", got: ProjectDisplayName("/home/user/src/Projmux"), want: "Projmux"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.got != tt.want {
				t.Fatalf("got %q, want %q", tt.got, tt.want)
			}
		})
	}
}

func TestSanitizeNameBaseCollapsesUnsupportedRunesAndRejectsEmptyResults(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		seed string
		want string
	}{
		{name: "plain seed is preserved with case", seed: "Projmux", want: "Projmux"},
		{name: "spaces collapse into a single dash", seed: "my   project", want: "my-project"},
		{name: "path separators collapse", seed: "src/app", want: "src-app"},
		{name: "selector punctuation collapses", seed: "role=lead,team", want: "role-lead-team"},
		{name: "leading and trailing dashes are trimmed", seed: "  --tool--  ", want: "tool"},
		{name: "punctuation only yields an empty base", seed: "///", want: ""},
		{name: "empty seed yields an empty base", seed: "", want: ""},
		{name: "dot names are rejected", seed: "..", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := SanitizeNameBase(tt.seed); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateNameRejectsValuesThatCannotBeStableQueryKeys(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "simple name", value: "projmux"},
		{name: "name with digits and dashes", value: "codex-12"},
		{name: "name with underscore and dot", value: "web_app.v2"},
		{name: "non-ascii name", value: "프로젝트"},
		{name: "empty", value: "", wantErr: true},
		{name: "whitespace inside", value: "my project", wantErr: true},
		{name: "path separator", value: "a/b", wantErr: true},
		{name: "selector comma", value: "a,b", wantErr: true},
		{name: "selector equals", value: "a=b", wantErr: true},
		{name: "dot", value: ".", wantErr: true},
		{name: "dotdot", value: "..", wantErr: true},
		{name: "leading space", value: " x", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateName(tt.value)
			if tt.wantErr != (err != nil) {
				t.Fatalf("ValidateName(%q) error = %v, wantErr %v", tt.value, err, tt.wantErr)
			}
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidName) {
					t.Fatalf("error %v is not ErrInvalidName", err)
				}
				if !IsUsageError(err) {
					t.Fatalf("invalid name must be a usage error: %v", err)
				}
			}
		})
	}
}

func TestAutomaticNameAllocationReservesTheLowestFreeSuffixPersistentlyAndIndependentlyOfScanOrder(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	first, err := reg.allocateName("test", "", KindProject, "Projmux", "uid-a")
	if err != nil {
		t.Fatalf("allocate first: %v", err)
	}
	second, err := reg.allocateName("test", "", KindProject, "Projmux", "uid-b")
	if err != nil {
		t.Fatalf("allocate second: %v", err)
	}
	third, err := reg.allocateName("test", "", KindProject, "Projmux", "uid-c")
	if err != nil {
		t.Fatalf("allocate third: %v", err)
	}
	if first != "Projmux" || second != "Projmux-1" || third != "Projmux-2" {
		t.Fatalf("got %q/%q/%q, want Projmux/Projmux-1/Projmux-2", first, second, third)
	}
	if len(reg.NameReservations) != 3 {
		t.Fatalf("reservations = %d, want 3 persisted entries", len(reg.NameReservations))
	}

	// Reversing the reservation slice must not change the next allocation:
	// the allocator scans integer suffixes, never the reservation order.
	reversed := NewRegistry()
	for i := len(reg.NameReservations) - 1; i >= 0; i-- {
		reversed.NameReservations = append(reversed.NameReservations, reg.NameReservations[i])
	}
	fourth, err := reversed.allocateName("test", "", KindProject, "Projmux", "uid-d")
	if err != nil {
		t.Fatalf("allocate fourth: %v", err)
	}
	if fourth != "Projmux-3" {
		t.Fatalf("fourth = %q, want Projmux-3", fourth)
	}

	// A released reservation frees exactly that suffix and nothing else.
	reg.releaseNames("uid-b")
	reused, err := reg.allocateName("test", "", KindProject, "Projmux", "uid-e")
	if err != nil {
		t.Fatalf("allocate reused: %v", err)
	}
	if reused != "Projmux-1" {
		t.Fatalf("reused = %q, want the freed Projmux-1", reused)
	}
}

func TestNameScopesAreRegistryWideForProjectsAndOwnerScopedOtherwise(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		kind     Kind
		ownerUID string
		want     string
	}{
		{name: "project ignores its owner", kind: KindProject, ownerUID: "ignored", want: ""},
		{name: "window scopes to its project", kind: KindWindow, ownerUID: "project-01", want: "project-01"},
		{name: "pane scopes to its owner", kind: KindPane, ownerUID: "window-01", want: "window-01"},
		{name: "agent scopes to its window", kind: KindAgent, ownerUID: "window-01", want: "window-01"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := scopeFor(tt.kind, tt.ownerUID); got != tt.want {
				t.Fatalf("scopeFor(%s, %q) = %q, want %q", tt.kind, tt.ownerUID, got, tt.want)
			}
		})
	}
}

func TestExplicitNameCollisionFailsWithoutAnImplicitSuffix(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	if _, err := reg.allocateName("test", "", KindProject, "shared", "uid-a"); err != nil {
		t.Fatalf("seed allocation: %v", err)
	}
	before := len(reg.NameReservations)

	err := reg.reserveExplicitName("rename project", "", KindProject, "shared", "uid-b")
	if err == nil {
		t.Fatal("explicit collision must fail")
	}
	if !errors.Is(err, ErrNameConflict) {
		t.Fatalf("error %v is not ErrNameConflict", err)
	}
	if !IsUsageError(err) {
		t.Fatalf("explicit collision must be a usage error: %v", err)
	}
	if len(reg.NameReservations) != before {
		t.Fatalf("reservations changed on a failed explicit rename: %d -> %d", before, len(reg.NameReservations))
	}
	if owner, _ := reg.nameOwner("", KindProject, "shared"); owner != "uid-a" {
		t.Fatalf("owner = %q, want uid-a", owner)
	}
}

func TestDerivePaneDisplayTitleUsesTopicShellThenRawTitleAndNeverThePaneName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		agent    string
		topic    string
		command  string
		rawTitle string
		want     string
	}{
		{name: "agent topic wins for an agent pane", agent: "codex", topic: "refactor", command: "node", rawTitle: "raw", want: "refactor"},
		{name: "topic is ignored without an agent", topic: "refactor", command: "zsh", rawTitle: "raw", want: "zsh"},
		{name: "known shell beats the raw title", command: "fish", rawTitle: "~/src", want: "fish"},
		{name: "raw title is the last resort", command: "node", rawTitle: "vite dev", want: "vite dev"},
		{name: "nothing derives an empty title", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := DerivePaneDisplayTitle(tt.agent, tt.topic, tt.command, tt.rawTitle); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
