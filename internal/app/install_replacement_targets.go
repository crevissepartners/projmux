package app

import (
	"debug/buildinfo"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/crevissepartners/projmux/internal/i18n"
)

// installReplacementTarget is transient terminal evidence, never a state-file
// field. It names only residual processes the existing policy allows to drain.
type installReplacementTarget struct {
	role     string
	pid      int
	revision string
}

func defaultInstallReplacementTargets() []installReplacementTarget {
	executable, err := os.Executable()
	if err != nil {
		return nil
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		resolved = executable
	}
	images, supported := defaultCodexProcessImages()
	if !supported {
		return nil
	}
	return projectInstallReplacementTargets(resolved, os.Getpid(), images, installReplacementProcessRevision)
}

func projectInstallReplacementTargets(self string, selfPID int, images []codexProcessImage, revision func(codexProcessImage) string) []installReplacementTarget {
	var targets []installReplacementTarget
	for _, image := range images {
		path, replaced := codexProcessImagePath(image.Exe)
		role := projmuxProcessRole(image.Cmdline)
		if image.PID <= 0 || image.PID == selfPID || self == "" || path != self || !replaced || replacementRoleDisposition(role) != replacementDispositionDrain {
			continue
		}
		rev := ""
		if revision != nil {
			rev = revision(image)
		}
		if strings.TrimSpace(rev) == "" {
			rev = "unknown"
		}
		targets = append(targets, installReplacementTarget{role: role, pid: image.PID, revision: rev})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].pid < targets[j].pid })
	return targets
}

// Only Linux's process reader produces targets. Read the mapped executable,
// not its former path, which now points at the newly installed binary. A
// missing build revision (including module installs), a vanished process, or a
// changed executable link yields unknown rather than the install's revision.
func installReplacementProcessRevision(image codexProcessImage) string {
	path := fmt.Sprintf("/proc/%d/exe", image.PID)
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return ""
	}
	if exe, err := os.Readlink(path); err != nil || exe != image.Exe {
		return ""
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			return setting.Value
		}
	}
	return ""
}

func renderInstallReplacementFailure(targets []installReplacementTarget, locale i18n.Locale) string {
	var out strings.Builder
	out.WriteString("   " + localizeText(locale, i18n.KeyInstallReplacementIncomplete,
		"The binary and config are already installed; broker replacement is incomplete.") + "\n")
	if len(targets) == 0 {
		out.WriteString("   " + localizeText(locale, i18n.KeyInstallReplacementUnknown,
			"No remaining target could be identified on recheck; the process table may have changed.") + "\n")
	} else {
		out.WriteString("   " + localizeText(locale, i18n.KeyInstallReplacementRemaining,
			"Remaining targets at failure recheck:") + "\n")
		for _, target := range targets {
			fmt.Fprintf(&out, "     role=%s pid=%d revision=%s\n", target.role, target.pid, target.revision)
		}
	}
	out.WriteString("   " + localizeText(locale, i18n.KeyInstallReplacementImpact,
		"Codex Agents created afterward may have no control while the old broker remains.") + "\n")
	out.WriteString("   " + localizeText(locale, i18n.KeyInstallReplacementRecovery,
		"Let existing Codex work finish, check that the listed processes exit naturally, then retry make install. If they remain, have the operator review the targets before deciding on termination.") + "\n")
	return out.String()
}
