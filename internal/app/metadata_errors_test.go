package app

import (
	"errors"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// metadataFixture builds a registry holding one project so collision and
// rebind failures can be produced through the real metadata operations rather
// than through hand-rolled error values.
func metadataFixture(t *testing.T) (coremetadata.Mutator, coremetadata.Registry) {
	t.Helper()
	roots := map[string]bool{"/src/projmux": true, "/src/other": true}
	counts := 0
	m := coremetadata.Mutator{
		Now: func() time.Time { return time.Date(2026, 8, 15, 9, 30, 0, 0, time.UTC) },
		NewUID: func(kind coremetadata.Kind) (string, error) {
			counts++
			return string(kind) + "-" + string(rune('a'+counts%26)), nil
		},
		DirExists: func(path string) (bool, error) { return roots[path], nil },
	}
	reg := coremetadata.NewRegistry()
	if _, err := m.RegisterProject(&reg, coremetadata.RegisterProjectOptions{
		Root:         "/src/projmux",
		DefaultShell: "/bin/zsh",
		OperationID:  "op-fixture",
	}); err != nil {
		t.Fatalf("seed register: %v", err)
	}
	return m, reg
}

func TestMapMetadataErrorRoutesInvalidInputToTheUsageErrorExitPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		produce      func(t *testing.T) error
		wantUsage    bool
		wantSentinel error
	}{
		{
			name:    "nil stays nil",
			produce: func(*testing.T) error { return nil },
		},
		{
			name: "explicit project name collision is a usage error",
			produce: func(t *testing.T) error {
				m, reg := metadataFixture(t)
				_, err := m.RegisterProject(&reg, coremetadata.RegisterProjectOptions{
					Root:         "/src/other",
					Name:         "projmux",
					DefaultShell: "/bin/zsh",
				})
				return err
			},
			wantUsage:    true,
			wantSentinel: coremetadata.ErrNameConflict,
		},
		{
			name: "rebind root collision is a usage error",
			produce: func(t *testing.T) error {
				m, reg := metadataFixture(t)
				result, err := m.RegisterProject(&reg, coremetadata.RegisterProjectOptions{Root: "/src/other", DefaultShell: "/bin/zsh"})
				if err != nil {
					t.Fatalf("seed second project: %v", err)
				}
				_, err = m.RebindProjectRoot(&reg, result.Project.Metadata.UID, "/src/projmux")
				return err
			},
			wantUsage:    true,
			wantSentinel: coremetadata.ErrRootConflict,
		},
		{
			name: "missing rebind root is a usage error",
			produce: func(t *testing.T) error {
				m, reg := metadataFixture(t)
				project, _ := reg.ProjectByRoot("/src/projmux")
				_, err := m.RebindProjectRoot(&reg, project.Metadata.UID, "/src/gone")
				return err
			},
			wantUsage:    true,
			wantSentinel: coremetadata.ErrInvalidRoot,
		},
		{
			name:         "invalid resource name is a usage error",
			produce:      func(*testing.T) error { return coremetadata.ValidateName("bad name") },
			wantUsage:    true,
			wantSentinel: coremetadata.ErrInvalidName,
		},
		{
			name: "a newer registry schema is a runtime error, not a usage error",
			produce: func(*testing.T) error {
				_, err := coremetadata.ClassifySchemaVersion(coremetadata.SchemaVersion + 1)
				return err
			},
			wantSentinel: coremetadata.ErrSchemaTooNew,
		},
		{
			name: "an unknown uid is a runtime error, not a usage error",
			produce: func(t *testing.T) error {
				m, reg := metadataFixture(t)
				_, err := m.RenameProject(&reg, "nope", "x")
				return err
			},
			wantSentinel: coremetadata.ErrNotFound,
		},
		{
			name:    "an unrelated error passes through untouched",
			produce: func(*testing.T) error { return errors.New("disk on fire") },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			source := tt.produce(t)
			mapped := MapMetadataError(source)

			if source == nil {
				if mapped != nil {
					t.Fatalf("nil must stay nil, got %v", mapped)
				}
				return
			}
			if source != nil && tt.wantSentinel != nil && !errors.Is(source, tt.wantSentinel) {
				t.Fatalf("produced error %v does not wrap %v", source, tt.wantSentinel)
			}
			if got := IsUsageError(mapped); got != tt.wantUsage {
				t.Fatalf("IsUsageError(%v) = %v, want %v", mapped, got, tt.wantUsage)
			}
			if mapped.Error() != source.Error() {
				t.Fatalf("mapping changed the message: %q -> %q", source.Error(), mapped.Error())
			}
		})
	}
}
