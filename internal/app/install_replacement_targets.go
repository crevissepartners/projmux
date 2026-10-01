package app

import (
	"bufio"
	"bytes"
	"debug/buildinfo"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/i18n"
)

// installReplacementTarget is transient terminal evidence, never a state-file
// field. It names only residual processes the existing policy allows to drain.
type installReplacementTarget struct {
	role     string
	pid      int
	revision string
	origin   installReplacementTargetOrigin
}

// installReplacementTargetOrigin says whose process one target is: the state
// domain it serves, the home it runs under, and whether that domain is the one
// this install-replace pass serves. A broker of another state domain (an
// isolated probe, say) is never reachable from this domain's discovery, so the
// verdict is what separates "my broker would not stand down" from "somebody
// else's broker runs this binary". Like the rest of the target, it is printed
// once and never persisted.
type installReplacementTargetOrigin struct {
	// domain is installReplacementDomainThis, Other, or Unknown.
	domain string
	// stateDomain and home are absolute paths, or empty when unreadable.
	stateDomain string
	home        string
}

const (
	installReplacementDomainThis    = "this"
	installReplacementDomainOther   = "other"
	installReplacementDomainUnknown = "unknown"
)

// installReplacementEnviron holds the only two environment values the origin
// reader keeps from a target process. Every other entry is discarded while it
// is read: a process environment carries credentials, and this output is a
// terminal line.
type installReplacementEnviron struct {
	home      string
	stateHome string
}

// installReplacementEnvironLimit bounds one environ read. The kernel caps a
// process's initial argv plus environment far below this on ordinary systems,
// and HOME sits among the first entries a login shell exports.
const installReplacementEnvironLimit = 1 << 20

func defaultInstallReplacementTargets() []installReplacementTarget {
	executorDomain, err := codexBrokerStateDomain(os.Getenv, os.UserHomeDir)
	if err != nil {
		executorDomain = ""
	}
	return readInstallReplacementTargets(installReplacementProcessRevision,
		installReplacementTargetOriginReader(executorDomain, readInstallReplacementEnviron))
}

func readInstallReplacementTargets(revision func(codexProcessImage) string, origin func(codexProcessImage) installReplacementTargetOrigin) []installReplacementTarget {
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
	return projectInstallReplacementTargets(resolved, os.Getpid(), images, revision, origin)
}

func projectInstallReplacementTargets(self string, selfPID int, images []codexProcessImage, revision func(codexProcessImage) string, origin func(codexProcessImage) installReplacementTargetOrigin) []installReplacementTarget {
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
		target := installReplacementTarget{role: role, pid: image.PID, revision: rev}
		if origin != nil {
			target.origin = origin(image)
		}
		targets = append(targets, target)
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

// installReplacementTargetOriginReader resolves each target's origin against
// executorDomain, the state domain this pass serves ("" when it could not be
// resolved, which makes every verdict unknown).
func installReplacementTargetOriginReader(executorDomain string, environ func(codexProcessImage) (installReplacementEnviron, bool)) func(codexProcessImage) installReplacementTargetOrigin {
	return func(image codexProcessImage) installReplacementTargetOrigin {
		var env installReplacementEnviron
		readable := false
		if environ != nil {
			env, readable = environ(image)
		}
		return resolveInstallReplacementTargetOrigin(executorDomain, image.Cmdline, env, readable)
	}
}

// resolveInstallReplacementTargetOrigin names one target's state domain the way
// that process resolved its own: an explicit --state-domain on its argv first
// (how a managed broker is launched), else its HOME and XDG_STATE_HOME through
// the same config.Homes rule every projmux process uses. HOME is reported only
// from a readable environment. The verdict compares cleaned absolute paths and
// is unknown whenever either side is missing.
func resolveInstallReplacementTargetOrigin(executorDomain string, cmdline []string, env installReplacementEnviron, readable bool) installReplacementTargetOrigin {
	var origin installReplacementTargetOrigin
	if readable && filepath.IsAbs(env.home) {
		origin.home = filepath.Clean(env.home)
	}
	if domain, ok := installReplacementArgvStateDomain(cmdline); ok {
		origin.stateDomain = domain
	} else if readable {
		paths, err := config.Homes{HomeDir: env.home, StateHome: env.stateHome}.Paths()
		if err == nil && filepath.IsAbs(paths.StateDir) {
			origin.stateDomain = filepath.Clean(paths.StateDir)
		}
	}
	executorDomain = strings.TrimSpace(executorDomain)
	switch {
	case origin.stateDomain == "" || !filepath.IsAbs(executorDomain):
		origin.domain = installReplacementDomainUnknown
	case origin.stateDomain == filepath.Clean(executorDomain):
		origin.domain = installReplacementDomainThis
	default:
		origin.domain = installReplacementDomainOther
	}
	return origin
}

// installReplacementArgvStateDomain reads an absolute --state-domain from the
// route words of one argv. Words behind a bare `--` are a caller's text and are
// never read; a relative value is one the broker itself would refuse.
func installReplacementArgvStateDomain(cmdline []string) (string, bool) {
	words := projmuxProcessRouteWords(cmdline)
	for index, word := range words {
		name, value, inline := strings.Cut(word, "=")
		if name != "--state-domain" && name != "-state-domain" {
			continue
		}
		if !inline {
			if index+1 >= len(words) {
				return "", false
			}
			value = words[index+1]
		}
		if !filepath.IsAbs(value) {
			return "", false
		}
		return filepath.Clean(value), true
	}
	return "", false
}

// readInstallReplacementEnviron reads HOME and XDG_STATE_HOME from one target's
// environment and discards every other entry as it goes. Like the revision
// reader, it answers unreadable when the executable link no longer names the
// census image, so a recycled pid cannot lend its environment to a target.
func readInstallReplacementEnviron(image codexProcessImage) (installReplacementEnviron, bool) {
	if image.PID <= 0 {
		return installReplacementEnviron{}, false
	}
	pid := strconv.Itoa(image.PID)
	// #nosec G304 -- pid is a positive integer rendered here; the fixed procfs
	// path cannot carry caller traversal.
	file, err := os.Open("/proc/" + pid + "/environ")
	if err != nil {
		return installReplacementEnviron{}, false
	}
	defer file.Close()
	env, err := scanInstallReplacementEnviron(io.LimitReader(file, installReplacementEnvironLimit))
	if err != nil {
		return installReplacementEnviron{}, false
	}
	if exe, err := os.Readlink("/proc/" + pid + "/exe"); err != nil || exe != image.Exe {
		return installReplacementEnviron{}, false
	}
	return env, true
}

// scanInstallReplacementEnviron keeps HOME and XDG_STATE_HOME from a
// NUL-separated environment block. Each other entry is dropped as soon as its
// name is known.
func scanInstallReplacementEnviron(r io.Reader) (installReplacementEnviron, error) {
	var env installReplacementEnviron
	reader := bufio.NewReader(r)
	for {
		entry, err := reader.ReadBytes(0)
		entry = bytes.TrimSuffix(entry, []byte{0})
		if name, value, ok := bytes.Cut(entry, []byte("=")); ok {
			switch string(name) {
			case "HOME":
				env.home = string(value)
			case config.XDGStateHomeVar:
				env.stateHome = string(value)
			}
		}
		if errors.Is(err, io.EOF) {
			return env, nil
		}
		if err != nil {
			return installReplacementEnviron{}, err
		}
	}
}

// renderInstallReplacementTargetValue prints one target field. A missing value
// is unknown, and a path with spaces, quotes, or control characters is quoted
// so it cannot split the line or reach the terminal as a control sequence.
func renderInstallReplacementTargetValue(value string) string {
	if value == "" {
		return installReplacementDomainUnknown
	}
	for _, r := range value {
		if r == '"' || r == '\\' || unicode.IsSpace(r) || !unicode.IsPrint(r) {
			return strconv.Quote(value)
		}
	}
	return value
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
			domain := target.origin.domain
			if domain == "" {
				domain = installReplacementDomainUnknown
			}
			fmt.Fprintf(&out, "     role=%s pid=%d revision=%s domain=%s stateDomain=%s home=%s\n",
				target.role, target.pid, target.revision, domain,
				renderInstallReplacementTargetValue(target.origin.stateDomain),
				renderInstallReplacementTargetValue(target.origin.home))
		}
	}
	out.WriteString("   " + localizeText(locale, i18n.KeyInstallReplacementImpact,
		"Codex Agents created afterward may have no control while the old broker remains.") + "\n")
	out.WriteString("   " + localizeText(locale, i18n.KeyInstallReplacementRecovery,
		"Let existing Codex work finish, check that the listed processes exit naturally, then retry make install. If they remain, have the operator review the targets before deciding on termination.") + "\n")
	return out.String()
}
