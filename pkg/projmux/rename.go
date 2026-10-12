package projmux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/core/selector"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/services/mutations"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

const (
	CodeRenameKindUnsupported Code = "rename-kind-unsupported"
	CodeMirrorNotConverged    Code = "mirror-not-converged"
	CodeProcessUnsupported    Code = "process-unsupported"
)

// Rename admits one explicit resource change. It never reads inherited TMUX or
// supplies an active target. Cancellation is effective before admission only.
func (c *Client) Rename(ctx context.Context, req core.RenameRequest) (core.RenameResult, error) {
	out := core.RenameResult{}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if !readKind(req.Kind) {
		return out, &Refusal{Code: CodeRenameKindUnsupported, message: fmt.Sprintf("RenameRequest.Kind %q is unsupported", req.Kind)}
	}
	if strings.TrimSpace(req.Ref) == "" {
		return out, &Refusal{Code: CodeSelectorInvalid, message: "RenameRequest.Ref must name one resource"}
	}
	if strings.TrimSpace(req.Name) == "" {
		return out, &Refusal{Code: CodeResourceNameInvalid, message: "RenameRequest.Name must name the resource"}
	}
	transport, err := resourcegraph.ResolveTransport(resourcegraph.TransportRequest{SocketPath: req.SocketPath})
	if err != nil {
		return out, &Refusal{Code: CodeRuntimeRequestInvalid, message: "RenameRequest.SocketPath must be absolute"}
	}
	socket := ""
	if transport.Present() {
		socket = transport.Value
	} else {
		socket = c.runtimeSocketPath
	}
	if c.pathsErr != nil {
		return out, resourceRefusal(c.pathsErr)
	}
	reg, err := resources.Load(c.paths)
	if err != nil {
		return out, resourceRefusal(err)
	}
	query, err := renameQuery(req)
	if err != nil {
		return out, resourceRefusal(err)
	}
	resolved, err := resources.Resolve(reg, metadata.RuntimeObservation{}, nil, metadata.Kind(req.Kind), query)
	if err != nil {
		return out, resourceRefusal(err)
	}
	if err := resources.Enforce(selector.Target{Verb: selector.VerbRename, Kind: metadata.Kind(req.Kind)}, query, resolved); err != nil {
		return out, resourceRefusal(err)
	}
	uid := resolved.Matches[0].UID
	if err := renameTargetAllowed(reg, metadata.Kind(req.Kind), uid); err != nil {
		return out, err
	}
	path := store.PathFor(c.paths.StateDir)
	s := store.NewStore(path)
	renamer := mutations.ExplicitSocketRenamer(mutations.NewStore(path), s.LoadReadOnly, socket, tmux.ExecRunner{})
	committed, notice, commitErr := renamer.CommitRename(ctx, metadata.Kind(req.Kind), uid, req.Name, func(_ context.Context, r metadata.Registry, _ metadata.Mutator) error {
		return renameTargetAllowed(r, metadata.Kind(req.Kind), uid)
	})
	if !committed {
		return out, renameRefusal(commitErr)
	}
	out.Committed = true
	out.Notice = notice
	b, err := json.Marshal(mutations.RenameReceipt(metadata.Kind(req.Kind), uid, req.Name))
	if err == nil {
		err = json.Unmarshal(b, &out.Receipt)
	}
	if err != nil {
		return out, err
	}
	reg, err = s.LoadReadOnly()
	if err != nil {
		return out, errors.Join(renameMirrorRefusal(commitErr), &Refusal{Code: CodeRegistryUnavailable, message: err.Error()})
	}
	out.Projection, err = publicResource(reg, metadata.Kind(req.Kind), uid)
	return out, errors.Join(renameMirrorRefusal(commitErr), err)
}
func renameMirrorRefusal(err error) error {
	if err == nil {
		return nil
	}
	return &Refusal{Code: CodeMirrorNotConverged, message: err.Error(), cause: err}
}
func renameRefusal(err error) error {
	if err == nil {
		return nil
	}
	var refusal *Refusal
	if errors.As(err, &refusal) {
		return err
	}
	var f *mutations.Failure
	if !errors.As(mutations.Classify(err), &f) {
		return err
	}
	message := f.Error()
	if f.Code == string(CodeResourceNameInvalid) || f.Code == string(CodeResourceNameConflict) || f.Code == string(CodeResourceNameExhausted) {
		message = "RenameRequest.Name: " + message
	}
	return &Refusal{Code: Code(f.Code), message: message, cause: err}
}
func renameQuery(req core.RenameRequest) (selector.Query, error) {
	var q selector.Query
	parse := func(kind metadata.Kind, value, field string) (selector.Ref, error) {
		r, e := selector.ParseRef(kind, value)
		if e != nil {
			e = selectorInputError(e, field)
		}
		return r, e
	}
	if req.Project != "" {
		r, e := parse(metadata.KindProject, req.Project, "RenameRequest.Project")
		if e != nil {
			return q, e
		}
		q.Project = &r
	}
	if req.Window != "" {
		if req.Kind != core.KindPane && req.Kind != core.KindAgent {
			return q, &resources.Failure{Code: string(CodeSelectorInvalid), Cause: errors.New("RenameRequest.Window scopes only Panes and Agents")}
		}
		r, e := parse(metadata.KindWindow, req.Window, "RenameRequest.Window")
		if e != nil {
			return q, e
		}
		q.Windows = []selector.Ref{r}
	}
	r, e := parse(metadata.Kind(req.Kind), req.Ref, "RenameRequest.Ref")
	if e != nil {
		return q, e
	}
	switch req.Kind {
	case core.KindProject:
		if req.Project != "" {
			return q, &resources.Failure{Code: string(CodeSelectorInvalid), Cause: errors.New("RenameRequest.Project is not supported for Project rename")}
		}
		q.Project = &r
	case core.KindWindow:
		q.Windows = []selector.Ref{r}
	case core.KindPane:
		q.Panes = []selector.Ref{r}
	case core.KindAgent:
		q.Agents = []selector.Ref{r}
	}
	return q, nil
}
func renameTargetAllowed(r metadata.Registry, kind metadata.Kind, uid string) error {
	var windowUID string
	switch kind {
	case metadata.KindProject:
		return nil
	case metadata.KindWindow:
		windowUID = uid
	case metadata.KindPane:
		p, ok := r.Pane(uid)
		if !ok {
			return metadata.ErrNotFound
		}
		if p.Spec.Runtime.EffectiveKind() == metadata.RuntimeProcess {
			return &Refusal{Code: CodeProcessUnsupported, message: "RenameRequest.Ref selects a process Pane"}
		}
		w, ok := mutations.WindowForPane(r, p.Metadata.UID)
		if !ok {
			return metadata.ErrNotFound
		}
		windowUID = w.Metadata.UID
	case metadata.KindAgent:
		a, ok := r.Agent(uid)
		if !ok {
			return metadata.ErrNotFound
		}
		p, ok := r.Pane(a.Status.PaneRef)
		if ok && p.Spec.Runtime.EffectiveKind() == metadata.RuntimeProcess {
			return &Refusal{Code: CodeProcessUnsupported, message: "RenameRequest.Ref selects a process Agent"}
		}
		windowUID = a.Metadata.OwnerUID()
	}
	w, ok := r.Window(windowUID)
	if !ok {
		return metadata.ErrNotFound
	}
	if _, ok := r.Project(w.Metadata.OwnerUID()); !ok {
		return &Refusal{Code: CodeSelectorInvalid, message: "RenameRequest.Ref requires a Project scope"}
	}
	return nil
}
