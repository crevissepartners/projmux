package metadata

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectNameBaseRemainsOnlyForHistoricalRootLookup(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, root, want string }{
		{name: "basename", root: "/home/user/src/Projmux", want: "Projmux"},
		{name: "filesystem root", root: "/", want: "project"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ProjectNameBase(tt.root); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAutomaticNumericSuffixProducerInventoryIsEmpty(t *testing.T) {
	t.Parallel()

	files := []string{
		"names.go", "mutator.go", "agent.go", "agentlinkage.go",
		"controlsession.go", "legacy.go", "snapshot_projection.go",
	}
	banned := []string{
		"allocateName(", "nextAvailableName(", "WindowNameBase(",
		"PaneNameBase(", "ManagedPaneNameBase(", "AgentNameBase(",
		"LegacyWindowNameSeed(", "LegacyPaneNameSeed(", "nameBase :=",
	}
	for _, name := range files {
		data, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatal(err)
		}
		for _, snippet := range banned {
			if strings.Contains(string(data), snippet) {
				t.Errorf("automatic naming producer %q remains in %s", snippet, name)
			}
		}
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

func TestAutomaticNameAllocationUsesTheExactFullUIDAndRemintsCollisions(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	reg.NameReservations = append(reg.NameReservations, NameReservation{Kind: KindProject, Name: "project-collision", UID: "project-existing"})
	candidates := []string{"project-collision", "project-free"}
	m := Mutator{NewUID: func(Kind) (string, error) {
		uid := candidates[0]
		candidates = candidates[1:]
		return uid, nil
	}}
	uid, name, err := m.mintAndReserveName(&reg, "test", "", KindProject, "")
	if err != nil {
		t.Fatalf("allocate automatic name: %v", err)
	}
	if uid != "project-free" || name != uid {
		t.Fatalf("got uid/name %q/%q, want exact reminted uid project-free", uid, name)
	}
	if owner, _ := reg.nameOwner("", KindProject, uid); owner != uid {
		t.Fatalf("reservation owner = %q, want %q", owner, uid)
	}
}

func TestNameScopesAreRootWideAndKeepKindInTheKey(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	reg.Projects = append(reg.Projects, Project{Metadata: ObjectMeta{UID: "project-01", Name: "project-01"}})
	reg.ControlSessions = append(reg.ControlSessions, ControlSession{Metadata: ObjectMeta{UID: "control-01", Name: "control-01"}})
	reg.Windows = append(reg.Windows,
		Window{Metadata: ObjectMeta{UID: "window-project", Name: "window-project", OwnerRef: &OwnerRef{Kind: KindProject, UID: "project-01"}}},
		Window{Metadata: ObjectMeta{UID: "window-control", Name: "window-control", OwnerRef: &OwnerRef{Kind: KindControlSession, UID: "control-01"}}},
	)
	reg.Agents = append(reg.Agents, Agent{Metadata: ObjectMeta{UID: "agent-01", Name: "agent-01", OwnerRef: &OwnerRef{Kind: KindWindow, UID: "window-project"}}})
	tests := []struct {
		name     string
		kind     Kind
		ownerUID string
		want     string
	}{
		{name: "project ignores its owner", kind: KindProject, ownerUID: "ignored", want: ""},
		{name: "window scopes to its project", kind: KindWindow, ownerUID: "project-01", want: "project-01"},
		{name: "control Window scopes to ControlSession", kind: KindWindow, ownerUID: "control-01", want: "control-01"},
		{name: "pane scopes through its Window", kind: KindPane, ownerUID: "window-project", want: "project-01"},
		{name: "pane scopes through its Agent", kind: KindPane, ownerUID: "agent-01", want: "project-01"},
		{name: "agent scopes through its Window", kind: KindAgent, ownerUID: "window-project", want: "project-01"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := reg.scopeFor(tt.kind, tt.ownerUID)
			if err != nil || got != tt.want {
				t.Fatalf("scopeFor(%s, %q) = %q, %v; want %q", tt.kind, tt.ownerUID, got, err, tt.want)
			}
		})
	}
}

func TestRootWideReservationSameDifferentRootAndKindMatrix(t *testing.T) {
	t.Parallel()

	base := NewRegistry()
	base.Projects = []Project{
		{Metadata: ObjectMeta{UID: "project-a", Name: "project-a"}},
		{Metadata: ObjectMeta{UID: "project-b", Name: "project-b"}},
	}
	base.ControlSessions = []ControlSession{
		{Metadata: ObjectMeta{UID: "control-a", Name: "control-a"}},
	}
	base.Windows = []Window{
		{Metadata: ObjectMeta{UID: "window-a1", Name: "window-a1", OwnerRef: &OwnerRef{Kind: KindProject, UID: "project-a"}}},
		{Metadata: ObjectMeta{UID: "window-a2", Name: "window-a2", OwnerRef: &OwnerRef{Kind: KindProject, UID: "project-a"}}},
		{Metadata: ObjectMeta{UID: "window-b1", Name: "window-b1", OwnerRef: &OwnerRef{Kind: KindProject, UID: "project-b"}}},
		{Metadata: ObjectMeta{UID: "window-c1", Name: "window-c1", OwnerRef: &OwnerRef{Kind: KindControlSession, UID: "control-a"}}},
		{Metadata: ObjectMeta{UID: "window-c2", Name: "window-c2", OwnerRef: &OwnerRef{Kind: KindControlSession, UID: "control-a"}}},
	}
	base.putReservation("project-a", KindPane, "shared", "pane-existing")
	base.putReservation("control-a", KindPane, "control-shared", "control-pane-existing")

	tests := []struct {
		name      string
		ownerUID  string
		kind      Kind
		wantScope string
		conflict  bool
	}{
		{name: "same root same kind", ownerUID: "window-a2", kind: KindPane, wantScope: "project-a", conflict: true},
		{name: "different root same kind", ownerUID: "window-b1", kind: KindPane, wantScope: "project-b"},
		{name: "different root kind parity", ownerUID: "window-c1", kind: KindPane, wantScope: "control-a"},
		{name: "same root different kind", ownerUID: "window-a2", kind: KindAgent, wantScope: "project-a"},
		{name: "different root different kind", ownerUID: "window-b1", kind: KindAgent, wantScope: "project-b"},
		{name: "different root and kind parity", ownerUID: "window-c1", kind: KindAgent, wantScope: "control-a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reg := base.Clone()
			before := mustJSON(t, reg)
			err := reg.reserveExplicitName("matrix", tt.ownerUID, tt.kind, "shared", "candidate")
			if tt.conflict {
				if !errors.Is(err, ErrNameConflict) || mustJSON(t, reg) != before {
					t.Fatalf("collision = %v, registry changed=%t", err, mustJSON(t, reg) != before)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if owner, ok := reg.nameOwner(tt.wantScope, tt.kind, "shared"); !ok || owner != "candidate" {
				t.Fatalf("reservation in scope %q = %q/%t", tt.wantScope, owner, ok)
			}
		})
	}
	t.Run("ControlSession same root same kind", func(t *testing.T) {
		reg := base.Clone()
		before := mustJSON(t, reg)
		err := reg.reserveExplicitName("matrix", "window-c2", KindPane, "control-shared", "candidate")
		if !errors.Is(err, ErrNameConflict) || mustJSON(t, reg) != before {
			t.Fatalf("collision = %v, registry changed=%t", err, mustJSON(t, reg) != before)
		}
	})
}

func TestExplicitNameCollisionFailsWithoutAnImplicitSuffix(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	reg.putReservation("", KindProject, "shared", "uid-a")
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
