package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbundle"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexgenerationhost"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexupgrade"
	"github.com/crevissepartners/projmux/internal/version"
)

const (
	managedCodexProtocolVersion    uint32 = 2
	managedCodexRuntimeKeyBytes    int    = 16
	managedCodexSocketPathMaxBytes int    = 100
)

type codexManagedCurrentActivator interface {
	Ensure(context.Context) error
}

type managedCodexActivationError struct {
	Reason string
	Action string
	err    error
}

func (err *managedCodexActivationError) Error() string {
	message := "Codex managed generation activation: " + err.Reason
	if strings.TrimSpace(err.Action) != "" {
		message += "; operator action: " + err.Action
	}
	return message
}

func (err *managedCodexActivationError) Unwrap() error { return err.err }

type productionCodexManagedCurrentActivator struct {
	stateDir    string
	coordinator *codexupgrade.Coordinator
	probe       func(context.Context) codexappserver.Health
	lookPath    func(string) (string, error)
	lookupEnv   func(string) string
	homeDir     func() (string, error)
	lease       func(string, string, string, codexbundle.ProtocolRange) (codexbundle.Lease, error)
	activate    func(context.Context, codexupgrade.ManagedCurrentActivation) (codexupgrade.Journal, error)
}

func newProductionCodexManagedCurrentActivator(stateDir string, coordinator *codexupgrade.Coordinator) *productionCodexManagedCurrentActivator {
	return &productionCodexManagedCurrentActivator{stateDir: stateDir, coordinator: coordinator}
}

func (activator *productionCodexManagedCurrentActivator) Ensure(ctx context.Context) error {
	if activator == nil || !filepath.IsAbs(strings.TrimSpace(activator.stateDir)) || activator.coordinator == nil {
		return managedActivationRefusal("coordinator-unavailable", "run `projmux doctor --section integrations --json --verbose` and repair the reported Projmux state path", nil)
	}
	probe := activator.probe
	if probe == nil {
		probe = func(ctx context.Context) codexappserver.Health {
			return codexappserver.ProbeDefaultProxy(ctx, codexNativeThreadTimeout, version.String(), true)
		}
	}
	health := probe(ctx)
	if health.EndpointReadiness != codexappserver.EndpointReady || health.VersionRelation != codexappserver.VersionSkew ||
		health.InstallCapability != codexappserver.InstallCapabilityManagedReady ||
		(health.ManagerOwnership != codexappserver.ManagerUnmanaged && health.ManagerOwnership != codexappserver.ManagerManaged) ||
		!codexappserver.IsSafeDiagnosticVersion(health.CLIVersion) || !codexappserver.IsSafeDiagnosticVersion(health.ManagedVersion) ||
		!codexappserver.IsSafeDiagnosticVersion(health.RunningVersion) || health.CLIVersion != health.ManagedVersion ||
		health.ManagedVersion == health.RunningVersion {
		return managedActivationRefusal("default-upgrade-topology-unavailable", "run `projmux doctor --section integrations --json --verbose` and resolve the reported Codex endpoint/version condition", nil)
	}

	lookupEnv := activator.lookupEnv
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	homeDir := activator.homeDir
	if homeDir == nil {
		homeDir = os.UserHomeDir
	}
	stateDomainPath, stateDomainID, err := defaultCodexStateDomain(lookupEnv, homeDir)
	if err != nil {
		return managedActivationRefusal("state-domain-unavailable", "set CODEX_HOME to an existing absolute owner-private directory and retry", err)
	}
	if err := requireOwnerPrivateDirectory(stateDomainPath); err != nil {
		return managedActivationRefusal("state-domain-not-owner-private", fmt.Sprintf("run `chmod 700 %q` after verifying that it is the intended CODEX_HOME, then retry", stateDomainPath), err)
	}
	privateRoot, socketPath, err := managedCodexRuntimeLocation(activator.stateDir, stateDomainID, health.ManagedVersion)
	if err != nil {
		return managedActivationRefusal("managed-runtime-socket-too-long",
			fmt.Sprintf("set XDG_STATE_HOME to a shorter owner-private absolute path so managed Codex socket %q is at most %d bytes, then retry", socketPath, managedCodexSocketPathMaxBytes), err)
	}
	bundleStore := filepath.Join(filepath.Clean(activator.stateDir), "codex-generations", "bundles")
	if managedCodexPathsOverlap(stateDomainPath, privateRoot) || managedCodexPathsOverlap(stateDomainPath, bundleStore) || managedCodexPathsOverlap(privateRoot, bundleStore) {
		return managedActivationRefusal("managed-generation-roots-overlap",
			"set CODEX_HOME and XDG_STATE_HOME to disjoint owner-private absolute directories, then retry", nil)
	}
	lookPath := activator.lookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	executable, err := lookPath("codex")
	if err == nil {
		executable, err = filepath.Abs(executable)
	}
	if err == nil {
		executable, err = filepath.EvalSymlinks(executable)
	}
	if err != nil || !filepath.IsAbs(executable) {
		return managedActivationRefusal("managed-release-unavailable", "repair the installed Codex standalone release selected by PATH, then retry", err)
	}
	protocol := codexbundle.ProtocolRange{Min: managedCodexProtocolVersion, Max: managedCodexProtocolVersion}
	leaseRelease := activator.lease
	if leaseRelease == nil {
		leaseRelease = codexgenerationhost.LeaseStandaloneRelease
	}
	lease, err := leaseRelease(bundleStore, executable, health.ManagedVersion, protocol)
	if err != nil {
		return managedActivationRefusal("managed-release-bundle-unavailable", "reinstall the complete Codex standalone release (codex, codex-code-mode-host, rg, and bwrap) and retry", err)
	}

	targetEndpoint := coremetadata.CodexEndpointRef{StateDomainID: stateDomainID, EndpointGenerationID: "codex-" + health.ManagedVersion}
	oldEndpoint := coremetadata.CodexEndpointRef{StateDomainID: stateDomainID, EndpointGenerationID: "codex-" + health.RunningVersion}
	if err := createOwnerPrivateDirectory(privateRoot); err != nil {
		return managedActivationRefusal("private-runtime-unavailable", fmt.Sprintf("repair %q as an owner-private directory and retry", privateRoot), err)
	}
	target := codexupgrade.GenerationConfig{
		Endpoint: targetEndpoint, StateDomainPath: stateDomainPath, PrivateRoot: privateRoot,
		SocketPath: socketPath,
		LeaseRoot:  lease.Root, RequiredProtocol: protocol,
	}
	owner := codexgeneration.OwnerUnmanaged
	if health.ManagerOwnership == codexappserver.ManagerManaged {
		owner = codexgeneration.OwnerOfficialManaged
	}
	request := codexupgrade.ManagedCurrentActivation{
		OperationRef: managedCodexActivationOperationRef(stateDomainID, health.RunningVersion, health.ManagedVersion),
		OldEndpoint:  oldEndpoint, OldOwner: owner, OldVersion: health.RunningVersion,
		Target: target, TargetBundleID: lease.ID,
		TargetTUIPath: filepath.Join(lease.Root, "bin", "codex"), TargetVersion: health.ManagedVersion,
	}
	activate := activator.activate
	if activate == nil {
		activate = activator.coordinator.ActivateManagedCurrent
	}
	if _, err := activate(ctx, request); err != nil {
		return managedActivationRefusal("managed-current-not-activated", managedCodexActivationFailureAction(err, target), err)
	}
	return nil
}

func managedActivationRefusal(reason, action string, err error) error {
	return &managedCodexActivationError{Reason: reason, Action: action, err: err}
}

func managedCodexActivationOperationRef(stateDomainID, oldVersion, targetVersion string) string {
	sum := sha256.Sum256([]byte(stateDomainID + "\x00" + oldVersion + "\x00" + targetVersion))
	return "managed-activation-" + hex.EncodeToString(sum[:16])
}

func managedCodexRuntimeLocation(stateDir, stateDomainID, targetVersion string) (string, string, error) {
	stateDir = filepath.Clean(strings.TrimSpace(stateDir))
	if !filepath.IsAbs(stateDir) || strings.TrimSpace(stateDomainID) == "" || !codexappserver.IsSafeDiagnosticVersion(targetVersion) {
		return "", "", errors.New("managed Codex runtime identity is invalid")
	}
	sum := sha256.Sum256([]byte(stateDomainID + "\x00" + targetVersion))
	privateRoot := filepath.Join(stateDir, "g", hex.EncodeToString(sum[:managedCodexRuntimeKeyBytes]))
	socketPath := filepath.Join(privateRoot, "s")
	if len([]byte(socketPath)) > managedCodexSocketPathMaxBytes {
		return privateRoot, socketPath, errors.New("managed Codex runtime socket exceeds the platform-safe bound")
	}
	return privateRoot, socketPath, nil
}

func managedCodexPathsOverlap(first, second string) bool {
	if first == second {
		return true
	}
	contains := func(parent, child string) bool {
		relative, err := filepath.Rel(parent, child)
		return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
	}
	return contains(first, second) || contains(second, first)
}

func managedCodexActivationFailureAction(err error, target codexupgrade.GenerationConfig) string {
	switch codexgenerationhost.HostRefusalOf(err) {
	case codexgenerationhost.HostRefusalPrivateRootInvalid:
		return fmt.Sprintf("repair %q as a non-symlink directory owned by the current user with mode 0700, then retry", target.PrivateRoot)
	case codexgenerationhost.HostRefusalBundleIncomplete, codexgenerationhost.HostRefusalBundleDrift:
		return "reinstall the complete managed Codex standalone release (codex, codex-code-mode-host, rg, and bwrap), then retry"
	case codexgenerationhost.HostRefusalSocketOccupied:
		return fmt.Sprintf("inspect %q and remove it only after proving no live process owns that exact socket, then retry", target.SocketPath)
	case codexgenerationhost.HostRefusalLaunchFailed, codexgenerationhost.HostRefusalProcessExited, codexgenerationhost.HostRefusalReadinessFailed:
		return fmt.Sprintf("run the managed Codex app-server against exact socket %q to diagnose its startup failure, repair that release, then retry", target.SocketPath)
	case codexgenerationhost.HostRefusalLaunchProofMismatch:
		return fmt.Sprintf("inspect the managed generation authority under %q for replaced intent, process, socket, or lease identity, then retry", target.PrivateRoot)
	default:
		return "run `projmux doctor --section integrations --json --verbose` and perform the exact generation action it reports before retrying"
	}
}

func requireOwnerPrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || !directoryOwnedBy(info, os.Geteuid()) {
		return errors.New("directory must be owner-private mode 0700")
	}
	return nil
}

func directoryOwnedBy(info os.FileInfo, uid int) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == uid
}

func createOwnerPrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return requireOwnerPrivateDirectory(path)
}
