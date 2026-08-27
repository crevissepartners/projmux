package codexappserver

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// ObserveDefaultInstallCapability performs bounded, read-only observation of
// the Codex executable on PATH and the canonical managed standalone payload.
// It does not resolve symlinks, execute Codex, or expose either path.
func ObserveDefaultInstallCapability() InstallCapability {
	return observeInstallCapability(os.Getenv, os.UserHomeDir, exec.LookPath, os.Stat, runtime.GOOS)
}

func observeInstallCapability(
	getenv func(string) string,
	userHomeDir func() (string, error),
	lookPath func(string) (string, error),
	stat func(string) (os.FileInfo, error),
	goos string,
) InstallCapability {
	if _, err := lookPath("codex"); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return InstallCapabilityCLIMissing
		}
		return InstallCapabilityUnknown
	}
	codexHome := getenv("CODEX_HOME")
	if codexHome == "" {
		home, err := userHomeDir()
		if err != nil || !filepath.IsAbs(home) {
			return InstallCapabilityUnknown
		}
		codexHome = filepath.Join(home, ".codex")
	}
	if !filepath.IsAbs(codexHome) {
		return InstallCapabilityUnknown
	}
	binary := "codex"
	if goos == "windows" {
		binary += ".exe"
	}
	info, err := stat(filepath.Join(codexHome, "packages", "standalone", "current", "bin", binary))
	if errors.Is(err, os.ErrNotExist) {
		return InstallCapabilityExternalCLIOnly
	}
	if err != nil {
		return InstallCapabilityUnknown
	}
	if info.IsDir() || (goos != "windows" && info.Mode().Perm()&0o111 == 0) {
		return InstallCapabilityExternalCLIOnly
	}
	return InstallCapabilityManagedReady
}

func withInstallCapability(health Health, capability InstallCapability) Health {
	health.InstallCapability = capability
	return health
}
