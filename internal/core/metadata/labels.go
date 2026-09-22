package metadata

import (
	"maps"
	"strings"
)

// SetLabels applies one label write to exactly one existing resource and
// reports the resulting ObjectMeta.
//
// The write is the whole mutation: labels carry no reservation, no owner edge,
// and no runtime projection, so nothing outside metadata.labels moves and the
// caller has no mirror to converge afterwards. That is what separates this from
// RenameProject and its siblings, which have to reserve the new name inside a
// scope before they can store it.
//
// set overwrites an existing key. There is no overwrite guard: this route
// addresses exactly one resolved resource, so the accident the guard exists to
// prevent -- relabelling a set of objects at once -- cannot happen here.
// remove deletes a key and is idempotent, because "this key is not on this
// resource" is the same end state whether or not it was there before.
//
// Keys and values are stored exactly as given. A label value has no length or
// character-set rule anywhere in this package, and adding one here would give
// the Registry two different label contracts depending on which writer ran.
//
// changed reports whether the stored map actually differs afterwards, so a
// repeat invocation neither rewrites the Registry nor claims it did.
func (m Mutator) SetLabels(reg *Registry, kind Kind, uid string, set map[string]string, remove []string) (meta ObjectMeta, changed bool, err error) {
	op := "label " + strings.ToLower(string(kind))

	for key := range set {
		if strings.TrimSpace(key) == "" {
			return ObjectMeta{}, false, inputErr(op, ErrInvalidLabel, "a label key must not be empty")
		}
	}
	for _, key := range remove {
		if strings.TrimSpace(key) == "" {
			return ObjectMeta{}, false, inputErr(op, ErrInvalidLabel, "a label key must not be empty")
		}
		if _, ok := set[key]; ok {
			return ObjectMeta{}, false, inputErr(op, ErrInvalidLabel,
				"label key %q is both set and removed in one write", key)
		}
	}

	target, ok := reg.resourceMetadata(kind, uid)
	if !ok {
		return ObjectMeta{}, false, stateErr(op, ErrNotFound, "%s %q does not exist", strings.ToLower(string(kind)), uid)
	}

	labels := cloneStringMap(target.Labels)
	if labels == nil {
		labels = map[string]string{}
	}
	maps.Copy(labels, set)
	for _, key := range remove {
		delete(labels, key)
	}
	if len(labels) == 0 {
		labels = nil
	}
	if maps.Equal(labels, target.Labels) {
		return target.Clone(), false, nil
	}

	target.Labels = labels
	reg.UpdatedAt = m.clock()().UTC()
	return target.Clone(), true, nil
}

// resourceMetadata returns the in-place ObjectMeta of one resource, so a
// metadata-only mutation can write it without a per-kind copy-back.
func (r *Registry) resourceMetadata(kind Kind, uid string) (*ObjectMeta, bool) {
	switch kind {
	case KindProject:
		if project, ok := r.Project(uid); ok {
			return &project.Metadata, true
		}
	case KindWindow:
		if window, ok := r.Window(uid); ok {
			return &window.Metadata, true
		}
	case KindPane:
		if pane, ok := r.Pane(uid); ok {
			return &pane.Metadata, true
		}
	case KindAgent:
		if agent, ok := r.Agent(uid); ok {
			return &agent.Metadata, true
		}
	}
	return nil, false
}
