// Package mutations owns Registry changes shared by CLI and SDK.
package mutations

import (
	"context"
	"errors"
	"fmt"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/receipt"
	"github.com/crevissepartners/projmux/internal/core/selector"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"path/filepath"
	"strings"
)

// Store is a path-bound writer; callbacks also permit the CLI's in-memory tests.
// Update and UpdateConvergent retain the Registry store's locking, degraded-mode
// gate, validation and atomic replacement semantics.
type Store struct {
	Update                  func(func(*coremetadata.Registry) error) (coremetadata.Registry, error)
	UpdateConvergent        func(func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error)
	UpdateConvergentContext func(context.Context, func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error)
	Mutator                 func() coremetadata.Mutator
}

func NewStore(path string) Store {
	s := store.NewStore(path)
	return Store{Update: s.Update, UpdateConvergent: s.UpdateConvergent, UpdateConvergentContext: s.UpdateConvergentContext, Mutator: store.DefaultMutator}
}

// Failure preserves the CLI cause and input/runtime classification.
type Failure struct {
	AgentUID string `json:"agentUID,omitempty"`
	Code     string
	Cause    error
}

func (e *Failure) Error() string            { return e.Cause.Error() }
func (e *Failure) Unwrap() error            { return e.Cause }
func (e *Failure) MetadataUsageError() bool { return coremetadata.IsUsageError(e.Cause) }
func Classify(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var f *Failure
	if errors.As(err, &f) {
		return err
	}
	code := "registry-unavailable"
	switch {
	case errors.Is(err, coremetadata.ErrInvalidRoot):
		code = "project-root-invalid"
	case errors.Is(err, coremetadata.ErrInvalidName):
		code = "resource-name-invalid"
	case errors.Is(err, coremetadata.ErrNameConflict):
		code = "resource-name-conflict"
	case errors.Is(err, coremetadata.ErrNameExhausted):
		code = "resource-name-exhausted"
	case errors.Is(err, coremetadata.ErrInvalidLabel):
		code = "resource-label-invalid"
	case errors.Is(err, coremetadata.ErrNotFound):
		code = "resource-not-found"
	}
	var se *selector.SelectorError
	if errors.As(err, &se) {
		code = "resource-label-invalid"
	}
	return &Failure{Code: code, Cause: err}
}

// ProjectRoot validates the explicit bootstrap path before taking a write lock.
func ProjectRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", &coremetadata.InputError{Cause: coremetadata.ErrInvalidRoot, Detail: "create project requires --root <absolute-path>"}
	}
	if !filepath.IsAbs(root) {
		return "", &coremetadata.InputError{Cause: coremetadata.ErrInvalidRoot, Detail: fmt.Sprintf("create project: --root %q must be absolute", root)}
	}
	return filepath.Clean(root), nil
}
func NewOperationID() (string, error) { return store.NewOperationID() }

// RegisterProject accepts cancellation before admission. Once admitted, it
// completes the locked transaction without deriving a provider lifetime from ctx.
func RegisterProject(ctx context.Context, s Store, opts coremetadata.RegisterProjectOptions) (coremetadata.RegisterProjectResult, error) {
	if err := ctx.Err(); err != nil {
		return coremetadata.RegisterProjectResult{}, err
	}
	root, err := ProjectRoot(opts.Root)
	if err != nil {
		return coremetadata.RegisterProjectResult{}, Classify(err)
	}
	opts.Root = root
	if s.UpdateConvergent == nil || s.Mutator == nil {
		return coremetadata.RegisterProjectResult{}, fmt.Errorf("register project root: the resource store is not configured")
	}
	var result coremetadata.RegisterProjectResult
	_, _, err = s.UpdateConvergent(func(r *coremetadata.Registry) error {
		var e error
		result, e = ReuseOrRegisterProject(r, s.Mutator(), opts)
		return e
	})
	return result, Classify(err)
}

// RegisterProjectRoot is the sidebar's explicit bootstrap, with no runtime effects.
func RegisterProjectRoot(ctx context.Context, s Store, shell string, sessionNameFor func(string) string, root string) (coremetadata.Project, bool, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if !filepath.IsAbs(root) {
		return coremetadata.Project{}, false, fmt.Errorf("register project root: %q must be an absolute path", root)
	}
	if s.UpdateConvergent == nil || s.Mutator == nil {
		return coremetadata.Project{}, false, fmt.Errorf("register project root: the resource store is not configured")
	}
	id, err := NewOperationID()
	if err != nil {
		return coremetadata.Project{}, false, err
	}
	session := ""
	if sessionNameFor != nil {
		session = strings.TrimSpace(sessionNameFor(root))
	}
	result, err := RegisterProject(ctx, s, coremetadata.RegisterProjectOptions{Root: root, DefaultShell: shell, SessionName: session, OperationID: id})
	return result.Project, !result.Reused, err
}
func ProjectReceipt(result coremetadata.RegisterProjectResult) receipt.OperationReceipt {
	effects := receipt.ReceiptEffects{
		Identity:     receipt.IdentityCreated,
		Address:      receipt.AddressAllocated,
		Topology:     receipt.TopologyEstablished,
		DesiredState: receipt.DesiredStateCreated,
		Runtime:      receipt.RuntimeUnchanged,
		Focus:        receipt.FocusUnchanged,
	}
	action := receipt.ActionCreated
	if result.Reused {
		effects.Identity = receipt.IdentityReused
		effects.Address = receipt.AddressUnchanged
		effects.Topology = receipt.TopologyUnchanged
		effects.DesiredState = receipt.DesiredStateReused
		action = receipt.ActionReused
	}
	receipt := receipt.NewReceipt(receipt.OperationCreateProject, receipt.ReceiptTarget{
		Kind: string(coremetadata.KindProject),
		UID:  result.Project.Metadata.UID,
		Name: result.Project.Metadata.Name,
	}, effects)
	receipt.Add(string(coremetadata.KindProject), result.Project.Metadata.UID, result.Project.Metadata.Name, action)
	for _, window := range result.Windows {
		receipt.Add(string(coremetadata.KindWindow), window.Metadata.UID, window.Metadata.Name, action)
		receipt.SelectWindows(window.Metadata.UID)
	}
	for _, pane := range result.Panes {
		receipt.Add(string(coremetadata.KindPane), pane.Metadata.UID, pane.Metadata.Name, action)
	}
	return receipt
}

func ReuseOrRegisterProject(working *coremetadata.Registry, mutator coremetadata.Mutator, opts coremetadata.RegisterProjectOptions) (coremetadata.RegisterProjectResult, error) {
	if existing, ok := working.ProjectByRoot(opts.Root); ok {
		if err := coremetadata.ValidateExplicitNameReuse("register project", coremetadata.KindProject, existing.Metadata, opts.Name); err != nil {
			return coremetadata.RegisterProjectResult{}, err
		}
		windows := working.WindowsOf(existing.Metadata.UID)
		// The reuse branch reports the Panes too. Without them the repeat
		// receipt would list fewer resources than the first call for an
		// identical graph, which is the same "reuse looks different from what it
		// reused" defect one level down.
		var panes []coremetadata.Pane
		for _, window := range windows {
			panes = append(panes, working.PanesOf(window.Metadata.UID)...)
		}
		return coremetadata.RegisterProjectResult{
			Project:     existing.Clone(),
			Windows:     windows,
			Panes:       panes,
			Reused:      true,
			OperationID: opts.OperationID,
		}, nil
	}
	return mutator.RegisterProject(working, opts)
}
