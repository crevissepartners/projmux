package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// The install preflight: the one step of `make install` that runs before
// anything is published, and the only one that may stop an install.
//
// A process Agent's owner is a long-lived process that keeps the image it
// started with, exactly like every other role the residue census counts. An
// install never stops or replaces one. That is safe while the installed build
// and the owner still speak the same Registry schema and the same Claude
// coordination protocol, and it is not safe when either changes: an owner
// that cannot read the migrated Registry fails closed on its next write and
// loses its Wait evidence, and a Claude owner on another coordination version
// refuses every message the new clients send it.
//
// So the install stops before publication, while the binary and the live
// config are still the ones those owners run against, and says which owners to
// stop. It never stops one itself.

// installProcessOwner is one live process Agent owner, as the preflight and the
// residue census print it. It is terminal evidence only and never reaches a
// ledger.
type installProcessOwner struct {
	AgentName string
	AgentUID  string
	Provider  string
	// Revision is the owner's self-reported build revision, empty when the
	// owner predates control protocol v1, was built without one, or did not
	// answer.
	Revision string
	// Coordination is the owner's self-reported claudeCoordinationVersion, 0
	// when it is unknown for the same reasons.
	Coordination int
}

// installProcessOwnerObserver reads one live owner's observation.
type installProcessOwnerObserver func(coremetadata.Registry, coremetadata.Pane) (processHostObservation, bool)

// installProcessHostAlive reports whether the exact recorded owner process is
// still the one running under its pid.
type installProcessHostAlive func(coremetadata.ProcessIdentity) bool

// liveInstallProcessOwners lists the owners of the current process activations
// whose recorded host process is still alive with the same birth identity.
//
// The Registry is the authority rather than the process table: an owner the
// web server hosts runs inside an executable that is not this one, so no
// census keyed on this executable's path could see it.
func liveInstallProcessOwners(registry coremetadata.Registry, alive installProcessHostAlive, observe installProcessOwnerObserver) []installProcessOwner {
	var owners []installProcessOwner
	for _, pane := range registry.Panes {
		activation := pane.Status.Activation.Process
		if pane.Spec.Runtime.EffectiveKind() != coremetadata.RuntimeProcess || activation == nil || !activation.HostProcess.Valid() {
			continue
		}
		if alive == nil || !alive(activation.HostProcess) {
			continue
		}
		agent, ok := registry.Agent(activation.Binding.AgentUID)
		if !ok {
			continue
		}
		owner := installProcessOwner{AgentName: agent.Metadata.Name, AgentUID: agent.Metadata.UID, Provider: agent.Spec.Provider}
		if observe != nil {
			if view, ok := observe(registry, pane); ok {
				owner.Revision = view.hostRevision()
				if view.Protocol >= 1 && view.Coordination > 0 {
					owner.Coordination = view.Coordination
				}
			}
		}
		owners = append(owners, owner)
	}
	sort.SliceStable(owners, func(i, j int) bool { return owners[i].AgentUID < owners[j].AgentUID })
	return owners
}

// installProcessHostIsAlive compares the recorded identity with the kernel's.
func installProcessHostIsAlive(recorded coremetadata.ProcessIdentity) bool {
	current, _, err := localipc.Process(recorded.PID)
	return err == nil && current == recorded
}

// defaultLiveInstallProcessOwners reads the live Registry snapshot without any
// write and lists its live owners. An absent Registry has none.
func defaultLiveInstallProcessOwners() ([]installProcessOwner, error) {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return nil, err
	}
	path := intmetadata.PathFor(paths.StateDir)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	registry, err := intmetadata.NewStore(path).LoadSnapshot()
	if err != nil {
		return nil, err
	}
	return liveInstallProcessOwners(registry, installProcessHostIsAlive, func(registry coremetadata.Registry, pane coremetadata.Pane) (processHostObservation, bool) {
		return observeProcessHostOwner(registry, path, pane)
	}), nil
}

// readRegistryFileSchemaVersion reads the on-disk envelope version without
// migrating or validating anything. ok is false when there is no Registry
// file, and an error means the file exists but names no readable version.
func readRegistryFileSchemaVersion(path string) (version int, ok bool, err error) {
	// #nosec G304 -- the path is resolved from projmux's own state directory.
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return 0, false, nil
	}
	var envelope struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return 0, false, fmt.Errorf("read registry schemaVersion %s: %w", path, err)
	}
	return envelope.SchemaVersion, true, nil
}

func defaultRegistryFileSchemaVersion() (int, bool, error) {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return 0, false, err
	}
	return readRegistryFileSchemaVersion(intmetadata.PathFor(paths.StateDir))
}

// installPreflightCommand is the hidden `internal install-preflight` route.
// Every dependency is injected so a test exercises the verdict and its text
// without a Registry, a process table, or a live owner.
type installPreflightCommand struct {
	schemaVersion       int
	coordinationVersion int
	registryVersion     func() (int, bool, error)
	owners              func() ([]installProcessOwner, error)
}

func newInstallPreflightCommand() *installPreflightCommand {
	return &installPreflightCommand{
		schemaVersion:       coremetadata.SchemaVersion,
		coordinationVersion: claudeCoordinationVersion,
		registryVersion:     defaultRegistryFileSchemaVersion,
		owners:              defaultLiveInstallProcessOwners,
	}
}

func runInstallPreflight(args []string, stderr io.Writer) error {
	if len(args) != 0 {
		return usageError("internal install-preflight does not accept arguments")
	}
	return newInstallPreflightCommand().Run(stderr)
}

// installPreflightExitError carries the refusal's exit status after its report
// has been printed, without a second generic error line.
type installPreflightExitError struct{}

func (installPreflightExitError) Error() string { return "install preflight refused publication" }
func (installPreflightExitError) ExitCode() int { return 1 }

// Run decides whether this install may publish.
//
// It refuses only when this build changes a version a live process owner
// depends on: the Registry schema it would migrate the file to, or the Claude
// coordination version a live Claude owner reports. An owner that does not
// report its coordination version is unknown, and unknown does not stop an
// install; it is printed so the operator can see it.
func (c *installPreflightCommand) Run(stderr io.Writer) error {
	if c == nil {
		return nil
	}
	if stderr == nil {
		stderr = io.Discard
	}
	fileVersion, haveRegistry, versionErr := 0, false, error(nil)
	if c.registryVersion != nil {
		fileVersion, haveRegistry, versionErr = c.registryVersion()
	}
	if versionErr != nil {
		// A Registry this check cannot read is one the installed build cannot
		// read either; the refusal belongs to that build, not to this check.
		_, _ = fmt.Fprintf(stderr, ">> install preflight: registry schemaVersion unreadable (%v); not checked\n", versionErr)
		return nil
	}
	schemaChanges := haveRegistry && fileVersion < c.schemaVersion

	var owners []installProcessOwner
	var ownersErr error
	if c.owners != nil {
		owners, ownersErr = c.owners()
	}
	if ownersErr != nil {
		if schemaChanges {
			_, _ = fmt.Fprintf(stderr, ">> install stopped before publication: this build changes the Registry schemaVersion %d to %d, and the live process owners could not be listed: %v\n", fileVersion, c.schemaVersion, ownersErr)
			_, _ = io.WriteString(stderr, "   Binary and live config are unchanged. Stop every process Agent first, then run the install again.\n")
			return installPreflightExitError{}
		}
		_, _ = fmt.Fprintf(stderr, ">> install preflight: live process owners could not be listed (%v); no version changes\n", ownersErr)
		return nil
	}

	coordinationChanges := false
	for _, owner := range owners {
		if owner.Provider == aiModeClaude && owner.Coordination > 0 && owner.Coordination != c.coordinationVersion {
			coordinationChanges = true
		}
	}
	stop := len(owners) > 0 && (schemaChanges || coordinationChanges)
	if !stop {
		_, _ = fmt.Fprintf(stderr, ">> install preflight: %s; %d live process %s\n",
			c.versionSummary(fileVersion, haveRegistry), len(owners), pluralizeInstallOwners(len(owners)))
		return nil
	}

	var buf bytes.Buffer
	buf.WriteString(">> install stopped before publication: this build changes")
	if schemaChanges {
		fmt.Fprintf(&buf, " the Registry schemaVersion %d to %d", fileVersion, c.schemaVersion)
	}
	if coordinationChanges {
		if schemaChanges {
			buf.WriteString(" and")
		}
		fmt.Fprintf(&buf, " the Claude coordination version to %d", c.coordinationVersion)
	}
	verb := "depend"
	if len(owners) == 1 {
		verb = "depends"
	}
	fmt.Fprintf(&buf, ", and %d live process %s %s on it\n", len(owners), pluralizeInstallOwners(len(owners)), verb)
	buf.WriteString(renderInstallProcessOwnerRows(owners, c.coordinationVersion))
	buf.WriteString("   Binary and live config are unchanged. Stop each owner, then install again:\n")
	buf.WriteString("     end its foreground owner (Ctrl-C or close its stdin)\n")
	buf.WriteString("   After the install, `projmux agent resume <agent-ref>` starts each Agent on the installed build.\n")
	buf.WriteString("   `projmux delete agent <agent-ref>` also stops an owner, but it removes the Agent: it cannot be resumed.\n")
	_, _ = io.WriteString(stderr, buf.String())
	return installPreflightExitError{}
}

func (c *installPreflightCommand) versionSummary(fileVersion int, haveRegistry bool) string {
	schema := "no registry"
	if haveRegistry {
		schema = fmt.Sprintf("schemaVersion %d→%d", fileVersion, c.schemaVersion)
	}
	return fmt.Sprintf("%s, claudeCoordinationVersion %d", schema, c.coordinationVersion)
}

func pluralizeInstallOwners(count int) string {
	if count == 1 {
		return "owner"
	}
	return "owners"
}

// renderInstallProcessOwnerRows prints one line per owner: the Agent, its
// provider, the build it runs, and the coordination version it reported. A
// version this build would change is marked.
func renderInstallProcessOwnerRows(owners []installProcessOwner, coordinationVersion int) string {
	var buf bytes.Buffer
	for _, owner := range owners {
		revision := owner.Revision
		if revision == "" {
			revision = "unknown"
		}
		coordination := "coordination unknown"
		if owner.Coordination > 0 {
			coordination = "coordination " + strconv.Itoa(owner.Coordination)
			if owner.Provider == aiModeClaude && owner.Coordination != coordinationVersion {
				coordination += " (changes)"
			}
		}
		fmt.Fprintf(&buf, "     %s (uid:%s)  %s  revision %s  %s\n", owner.AgentName, owner.AgentUID, owner.Provider, revision, coordination)
	}
	return buf.String()
}
