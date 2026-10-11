package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/services/mutations"
)

// canonicalCreateProject is the explicit Project bootstrap route.
const canonicalCreateProject = "create project"

// runResourceProject registers exactly one filesystem path as a Registry Project.
//
// This route exists because Project registration used to have no route at all. A
// Project appeared as a side effect of any mutation: the reconcile prelude walked
// every discovered workdir and registered the ones it did not recognize, so
// `create pane` in one repository could add a dozen Projects for directories the
// operator had never opened. Discovery is a scan and a scan is not a decision.
//
// The decision is here, and it is narrow by construction:
//
//   - One --root, and only that root. Sibling directories under the same
//     discovery root stay unregistered.
//   - Registration is Registry-only. No tmux session, window or pane is
//     materialized, because existing is not the same as running; `attach project`
//     and the sidebar's open flow own activation.
//   - A path an existing Project already claims is reused, not duplicated, and
//     reusing writes nothing at all.
func (c *createCommand) runResourceProject(args []string, stdout, stderr io.Writer) error {
	const spelling = canonicalCreateProject

	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	setRouteUsage(fs)
	root := fs.String("root", "", "Absolute path of the Project root to register")
	name := fs.String("name", "", "Explicit registry-unique Project name")
	labels := repeatedFlag{}
	fs.Var(&labels, "label", "Repeatable key=value metadata label")
	output := fs.String("o", "", "Output projection")
	fs.StringVar(output, "output", "", "Output projection")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return flagParseError(err)
	}
	if fs.NArg() != 0 {
		return usageError(fmt.Sprintf("%s does not accept positional arguments; pass the root with --root", spelling))
	}
	mode, err := resolveLifecycleProjection(spelling, *output)
	if err != nil {
		return err
	}
	labelMapping, err := labelMap(labels)
	if err != nil {
		return MapMetadataError(err)
	}
	target, err := mutations.ProjectRoot(*root)
	if err != nil {
		return MapMetadataError(err)
	}

	if c == nil || c.store == nil || c.store.updateConvergent == nil {
		return fmt.Errorf("create: the resource-backed create routes are not configured")
	}
	operationID, err := c.newOperationID()
	if err != nil {
		return err
	}

	result, err := mutations.RegisterProject(context.Background(), projectMutationStore(c.store), coremetadata.RegisterProjectOptions{
		Root: target, Name: *name, Labels: labelMapping, DefaultShell: c.shell, SessionName: c.registerSessionName(target), OperationID: operationID,
	})
	if err != nil {
		return MapMetadataError(err)
	}

	action := cli.ActionCreated
	if result.Reused {
		action = cli.ActionReused
	}
	return c.writeResultsWithReceipt(stdout, spelling, mode, coremetadata.KindProject, []createResult{{
		kind:        coremetadata.KindProject,
		uid:         result.Project.Metadata.UID,
		name:        result.Project.Metadata.Name,
		projectName: result.Project.Metadata.Name,
		action:      action,
	}}, registerProjectReceipt(result))
}

func registerProjectReceipt(result coremetadata.RegisterProjectResult) cli.OperationReceipt {
	return cli.OperationReceipt(mutations.ProjectReceipt(result))
}
func (c *createCommand) registerSessionName(root string) string {
	if c == nil || c.sessionNameFor == nil {
		return ""
	}
	return strings.TrimSpace(c.sessionNameFor(root))
}
func projectMutationStore(store *resourceStore) mutations.Store {
	if store == nil {
		return mutations.Store{}
	}
	return mutations.Store{Update: store.update, UpdateConvergent: store.updateConvergent, Mutator: store.mutator}
}
func registerProjectRoot(ctx context.Context, store *resourceStore, shell string, sessionNameFor func(string) string, root string) (coremetadata.Project, bool, error) {
	// CLI historically completes sidebar bootstrap irrespective of caller cancellation.
	result, created, err := mutations.RegisterProjectRoot(context.WithoutCancel(ctx), projectMutationStore(store), shell, sessionNameFor, root)
	return result, created, MapMetadataError(err)
}
