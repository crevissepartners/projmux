package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RuntimeDiagnosticsVisibilityFileName holds the Projects sidebar's Runtime
// diagnostics row policy.
//
// It is a presentation preference and nothing else. The `runtime diagnostics`
// picker and `get runtime` never read it, so a saved value can hide a row and
// can never disable a capability.
const RuntimeDiagnosticsVisibilityFileName = "runtime-diagnostics-visibility"

// RuntimeDiagnosticsVisibility is how the Projects sidebar offers its Runtime
// diagnostics row.
type RuntimeDiagnosticsVisibility string

const (
	// RuntimeDiagnosticsWhenNeeded shows the row only when the exact host is
	// running something an operator would have to act on, or when the
	// observation that would say so could not be taken.
	RuntimeDiagnosticsWhenNeeded RuntimeDiagnosticsVisibility = "when-needed"
	// RuntimeDiagnosticsAlways keeps the row on every render, which is what the
	// surface did before the choice existed.
	RuntimeDiagnosticsAlways RuntimeDiagnosticsVisibility = "always"

	// RuntimeDiagnosticsVisibilityDefault is the effective value with nothing
	// saved. It is a read-time default: no install is migrated and no file is
	// written to adopt it.
	RuntimeDiagnosticsVisibilityDefault = RuntimeDiagnosticsWhenNeeded
)

// RuntimeDiagnosticsVisibilityOrigin says where an effective value came from.
//
// It exists so an unreadable or unrecognized saved value is reported as invalid
// rather than rendering silently as the default: the two produce the same
// behavior and a very different reason.
type RuntimeDiagnosticsVisibilityOrigin string

const (
	// RuntimeDiagnosticsVisibilityDefaulted means nothing is saved.
	RuntimeDiagnosticsVisibilityDefaulted RuntimeDiagnosticsVisibilityOrigin = "default"
	// RuntimeDiagnosticsVisibilitySaved means the file named a valid choice.
	RuntimeDiagnosticsVisibilitySaved RuntimeDiagnosticsVisibilityOrigin = "saved"
	// RuntimeDiagnosticsVisibilityInvalid means a file exists and does not name
	// a valid choice. The effective value is the default and nothing is written.
	RuntimeDiagnosticsVisibilityInvalid RuntimeDiagnosticsVisibilityOrigin = "invalid"
)

// RuntimeDiagnosticsVisibilityFile is the saved policy path.
func (p Paths) RuntimeDiagnosticsVisibilityFile() string {
	return filepath.Join(p.ConfigDir, RuntimeDiagnosticsVisibilityFileName)
}

// NormalizeRuntimeDiagnosticsVisibility parses one saved or chosen value.
//
// The second result is false for anything the closed choice does not name,
// which is what lets a caller report an invalid value instead of guessing one.
func NormalizeRuntimeDiagnosticsVisibility(value string) (RuntimeDiagnosticsVisibility, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(RuntimeDiagnosticsWhenNeeded):
		return RuntimeDiagnosticsWhenNeeded, true
	case string(RuntimeDiagnosticsAlways):
		return RuntimeDiagnosticsAlways, true
	default:
		return RuntimeDiagnosticsVisibilityDefault, false
	}
}

// LoadRuntimeDiagnosticsVisibilityFile reads the saved policy.
//
// It never writes. A missing file resolves to the default; an unreadable file or
// a value the choice does not name resolves to the default with an invalid
// origin, so the surface can say why it is not honoring what is on disk.
func LoadRuntimeDiagnosticsVisibilityFile(path string) (RuntimeDiagnosticsVisibility, RuntimeDiagnosticsVisibilityOrigin, error) {
	if strings.TrimSpace(path) == "" {
		return RuntimeDiagnosticsVisibilityDefault, RuntimeDiagnosticsVisibilityDefaulted, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RuntimeDiagnosticsVisibilityDefault, RuntimeDiagnosticsVisibilityDefaulted, nil
		}
		return RuntimeDiagnosticsVisibilityDefault, RuntimeDiagnosticsVisibilityInvalid,
			fmt.Errorf("read runtime diagnostics visibility file: %w", err)
	}
	mode, ok := NormalizeRuntimeDiagnosticsVisibility(string(content))
	if !ok {
		return RuntimeDiagnosticsVisibilityDefault, RuntimeDiagnosticsVisibilityInvalid, nil
	}
	return mode, RuntimeDiagnosticsVisibilitySaved, nil
}

// SaveRuntimeDiagnosticsVisibilityFile writes one valid choice atomically.
func SaveRuntimeDiagnosticsVisibilityFile(path string, value RuntimeDiagnosticsVisibility) error {
	if strings.TrimSpace(path) == "" {
		return ErrHomeDirRequired
	}
	mode, ok := NormalizeRuntimeDiagnosticsVisibility(string(value))
	if !ok {
		return fmt.Errorf("unknown runtime diagnostics visibility %q", string(value))
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create runtime diagnostics visibility directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create runtime diagnostics visibility temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.WriteString(string(mode) + "\n"); err != nil {
		tmp.Close()
		return fmt.Errorf("write runtime diagnostics visibility temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close runtime diagnostics visibility temp file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod runtime diagnostics visibility temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename runtime diagnostics visibility temp file: %w", err)
	}
	return nil
}
