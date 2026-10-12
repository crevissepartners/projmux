package projectcandidates

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	projdirEnvVar            = "PROJMUX_PROJDIR"
	managedRootsEnvVar       = "PROJMUX_MANAGED_ROOTS"
	legacyManagedRootsEnvVar = "TMUX_SESSIONIZER_ROOTS"
	projdirSourcePROJDIRenv  = "PROJMUX_PROJDIR env"
	projdirSourceTmuxOption  = "@projmux_projdir tmux"
	projdirSourceSaved       = "saved"
	projdirSourceUnresolved  = ""
)

// ResolveProjdir retains environment, tmux option and saved-root precedence.
func ResolveProjdir(
	homeDir string,
	lookup func(string) string,
	tmuxOption func() string,
	load func(string) (string, error),
) (string, string) {
	if raw := envValue(lookup, projdirEnvVar); strings.TrimSpace(raw) != "" {
		// PROJMUX_PROJDIR is a PATH-style multi-value: the first
		// non-empty entry is the primary repo root; extra entries are
		// fed to managed roots elsewhere via ExtraProjdirRoots.
		for _, entry := range filepath.SplitList(raw) {
			if repoRoot := cleanPath(entry); repoRoot != "" {
				return repoRoot, projdirSourcePROJDIRenv
			}
		}
	}

	if tmuxOption != nil {
		if raw := strings.TrimSpace(tmuxOption()); raw != "" {
			if repoRoot := cleanPath(raw); repoRoot != "" {
				return repoRoot, projdirSourceTmuxOption
			}
		}
	}

	if load != nil && homeDir != "" {
		if saved, err := load(homeDir); err == nil {
			if repoRoot := cleanPath(saved); repoRoot != "" {
				return repoRoot, projdirSourceSaved
			}
		}
	}

	return "", projdirSourceUnresolved
}

// ExtraProjdirRoots returns the remaining nonempty PROJMUX_PROJDIR entries.
func ExtraProjdirRoots(lookup func(string) string) []string {
	raw := envValue(lookup, projdirEnvVar)
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	entries := filepath.SplitList(raw)
	primaryFound := false
	extras := make([]string, 0, len(entries))
	for _, entry := range entries {
		cleaned := cleanPath(entry)
		if cleaned == "" {
			continue
		}
		if !primaryFound {
			primaryFound = true
			continue
		}
		extras = append(extras, cleaned)
	}
	return extras
}

// ManagedRoots retains the configured root order and fallback rules.
func ManagedRoots(homeDir, repoRoot string, ExtraProjdirRoots []string, lookup func(string) string, loadWorkdirs func(string) ([]string, error)) []string {
	roots := make([]string, 0)
	seen := make(map[string]struct{})

	appendRoot := func(root string) {
		root = cleanPath(root)
		if root == "" {
			return
		}
		if _, ok := seen[root]; ok {
			return
		}
		seen[root] = struct{}{}
		roots = append(roots, root)
	}

	// Extra PROJMUX_PROJDIR roots (everything past the primary entry)
	// are prepended so they take priority over env- or saved-managed
	// roots and the default fallback list. Like PROJMUX_MANAGED_ROOTS,
	// any explicit env-driven entry suppresses the saved-workdirs and
	// default fallback below.
	managedFromEnv := false
	for _, root := range ExtraProjdirRoots {
		if cleanPath(root) == "" {
			continue
		}
		managedFromEnv = true
		appendRoot(root)
	}

	for _, value := range []string{
		envValue(lookup, managedRootsEnvVar),
		envValue(lookup, legacyManagedRootsEnvVar),
	} {
		for _, root := range filepath.SplitList(value) {
			if cleanPath(root) == "" {
				continue
			}
			managedFromEnv = true
			appendRoot(root)
		}
	}

	if !managedFromEnv && loadWorkdirs != nil {
		saved, err := loadWorkdirs(homeDir)
		if err == nil {
			for _, root := range saved {
				appendRoot(root)
			}
		}
	}

	if len(roots) == 0 {
		for _, root := range DefaultManagedRoots(homeDir, repoRoot) {
			appendRoot(root)
		}
	}

	return roots
}

// DefaultManagedRoots returns the conventional roots in discovery order.
func DefaultManagedRoots(homeDir, repoRoot string) []string {
	roots := make([]string, 0, 6)
	for _, root := range []string{
		filepath.Join(homeDir, "source"),
		filepath.Join(homeDir, "work"),
		filepath.Join(homeDir, "projects"),
		filepath.Join(homeDir, "src"),
		filepath.Join(homeDir, "code"),
		repoRoot,
	} {
		root = cleanPath(root)
		if root == "" {
			continue
		}
		roots = append(roots, root)
	}

	return roots
}

func cleanPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	return filepath.Clean(path)
}
func envValue(lookup func(string) string, name string) string {
	if lookup == nil {
		return ""
	}
	return lookup(name)
}

// CaptureEnvironment captures the configured discovery roots once.
func CaptureEnvironment() map[string]string {
	out := map[string]string{}
	for _, key := range []string{projdirEnvVar, managedRootsEnvVar, legacyManagedRootsEnvVar} {
		out[key] = os.Getenv(key)
	}
	return out
}
