package app

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
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
	root := fs.String("root", "", "Absolute path of the Project root to register")
	name := fs.String("name", "", "Explicit registry-unique Project name")
	labels := repeatedFlag{}
	fs.Var(&labels, "label", "Repeatable key=value metadata label")
	output := fs.String("o", "", "Output projection")
	fs.StringVar(output, "output", "", "Output projection")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return usageError(fmt.Sprintf("%s does not accept positional arguments; pass the root with --root", spelling))
	}
	mode, err := c.resolveProjection(spelling, *output)
	if err != nil {
		return err
	}
	labelMapping, err := labelMap(labels)
	if err != nil {
		return MapMetadataError(err)
	}
	target := strings.TrimSpace(*root)
	if target == "" {
		return usageError(fmt.Sprintf("%s requires --root <absolute-path>", spelling))
	}
	if !filepath.IsAbs(target) {
		return usageError(fmt.Sprintf("%s: --root %q must be absolute", spelling, target))
	}
	target = filepath.Clean(target)

	if c == nil || c.store == nil || c.store.updateConvergent == nil {
		return fmt.Errorf("create: the resource-backed create routes are not configured")
	}
	operationID, err := c.newOperationID()
	if err != nil {
		return err
	}

	var result coremetadata.RegisterProjectResult
	// updateConvergent is the write-free half of the repeat contract: registering
	// an already-registered root produces an identical Registry, and an identical
	// Registry is not written.
	if _, _, err := c.store.updateConvergent(func(working *coremetadata.Registry) error {
		registered, err := reuseOrRegisterProject(working, c.store.mutator(), coremetadata.RegisterProjectOptions{
			Root:         target,
			Name:         strings.TrimSpace(*name),
			Labels:       labelMapping,
			DefaultShell: c.shell,
			SessionName:  c.registerSessionName(target),
			OperationID:  operationID,
		})
		if err != nil {
			return err
		}
		result = registered
		return nil
	}); err != nil {
		return MapMetadataError(err)
	}

	return c.writeResults(stdout, spelling, mode, coremetadata.KindProject, []createResult{{
		kind:        coremetadata.KindProject,
		uid:         result.Project.Metadata.UID,
		name:        result.Project.Metadata.Name,
		projectName: result.Project.Metadata.Name,
	}})
}

// registerSessionName projects the persistent session name a Project root maps
// onto. It records a projection, not a runtime: nothing is started here.
func (c *createCommand) registerSessionName(root string) string {
	if c == nil || c.sessionNameFor == nil {
		return ""
	}
	return strings.TrimSpace(c.sessionNameFor(root))
}

// registerProjectRoot is the shared explicit-bootstrap seam.
//
// It answers the question the sidebar's candidate open asks -- "which Project is
// this exact path, registering it if none is" -- with the same transaction and
// the same idempotence the `create project` route uses, so there is one
// registration implementation rather than a UI-flavored copy.
//
// It hands back the whole registered Project rather than only its uid because the
// open flow mirrors identity onto the session it is about to mint, and the
// identity mirror is uid *and* name. Narrowing the result to a uid here would
// force the caller to re-read the Registry for the other half of an identity this
// transaction already resolved.
func registerProjectRoot(ctx context.Context, store *resourceStore, shell string, sessionNameFor func(string) string, root string) (coremetadata.Project, bool, error) {
	_ = ctx
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" || !filepath.IsAbs(root) {
		return coremetadata.Project{}, false, fmt.Errorf("register project root: %q must be an absolute path", root)
	}
	if store == nil || store.updateConvergent == nil || store.mutator == nil {
		return coremetadata.Project{}, false, fmt.Errorf("register project root: the resource store is not configured")
	}
	operationID, err := newCreateOperationID()
	if err != nil {
		return coremetadata.Project{}, false, err
	}
	sessionName := ""
	if sessionNameFor != nil {
		sessionName = strings.TrimSpace(sessionNameFor(root))
	}
	var result coremetadata.RegisterProjectResult
	if _, _, err := store.updateConvergent(func(working *coremetadata.Registry) error {
		registered, err := reuseOrRegisterProject(working, store.mutator(), coremetadata.RegisterProjectOptions{
			Root:         root,
			DefaultShell: shell,
			SessionName:  sessionName,
			OperationID:  operationID,
		})
		if err != nil {
			return err
		}
		result = registered
		return nil
	}); err != nil {
		return coremetadata.Project{}, false, MapMetadataError(err)
	}
	return result.Project, !result.Reused, nil
}

// reuseOrRegisterProject answers an already-registered root from the Registry and
// registers only a root no Project claims.
//
// The lookup has to come first because registration validates the directory, and a
// Project whose spec.root has since disappeared is still a Project. Without this,
// re-opening a Project whose directory was moved away would fail at registration
// -- reporting the wrong problem, and reporting it before the surface that owns
// MissingRoot remediation gets a chance to.
func reuseOrRegisterProject(working *coremetadata.Registry, mutator coremetadata.Mutator, opts coremetadata.RegisterProjectOptions) (coremetadata.RegisterProjectResult, error) {
	if existing, ok := working.ProjectByRoot(opts.Root); ok {
		return coremetadata.RegisterProjectResult{
			Project:     existing.Clone(),
			Windows:     working.WindowsOf(existing.Metadata.UID),
			Reused:      true,
			OperationID: opts.OperationID,
		}, nil
	}
	return mutator.RegisterProject(working, opts)
}
