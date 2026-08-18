package runtimediag

import (
	"slices"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

// APIVersion is the schema version every runtime projection carries. It is the
// resource model's version deliberately: a runtime report is a different
// projection of the same world, not a second API.
const APIVersion = coremetadata.APIVersion

// Resource is the Registry resource one runtime object is bound to.
//
// It is present only on a managed row. A refused object -- recoverable,
// foreign, conflict -- never carries a resource identity here, because the
// whole point of refusing it is that projmux does not know whose it is.
type Resource struct {
	Kind string `json:"kind"`
	UID  string `json:"uid"`
	Name string `json:"name,omitempty"`
}

// Conflict is one recorded contradiction that names this row as a claimant.
type Conflict struct {
	UID     string   `json:"uid"`
	Reason  string   `json:"reason"`
	Detail  string   `json:"detail,omitempty"`
	Targets []string `json:"targets,omitempty"`
}

// Row is one observed tmux object.
//
// ID and Target are both handles and they are not interchangeable. ID is the
// stable tmux id and the only safe thing to store. Target is the fully
// qualified coordinate an operator (or the focus route) addresses the object
// by: a session name, `<session>:@N` for a window, `<session>:@N.%N` for a
// pane. The qualified spelling is built rather than reported because tmux's own
// `session:index` window target is an index, and an index moves when a window
// is closed beside it -- an id does not.
type Row struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	// Target is the fully qualified exact coordinate, empty only when the
	// containing session could not be observed.
	Target string `json:"target,omitempty"`
	// Name is what tmux displays: session name, window name, or pane title. It
	// is context for a human and is never an identity input.
	Name  string `json:"name,omitempty"`
	Class string `json:"class"`
	// UID is the projmux uid mirrored onto this object, empty when it carries
	// none. It is reported even for a refused object, because a uid this
	// Registry does not contain is exactly the evidence an operator needs.
	UID string `json:"uid,omitempty"`
	// ContainerID is the stable tmux id of the enclosing object.
	ContainerID string `json:"containerID,omitempty"`
	// SessionID is the stable id of the session this object lives in, resolved
	// through the containment chain. It is what groups a report by server
	// topology without a name join.
	SessionID string `json:"sessionID,omitempty"`
	// Resource is the bound Registry resource, managed rows only.
	Resource *Resource `json:"resource,omitempty"`
	// Reason states why this class, in one clause.
	Reason string `json:"reason,omitempty"`
	// Conflicts are the recorded contradictions naming this object.
	Conflicts []Conflict `json:"conflicts,omitempty"`
}

// Managed reports whether this row is bound to a Registry resource.
func (r Row) Managed() bool {
	return r.Class == string(resourcegraph.ClassManaged) && r.Resource != nil
}

// Transport is the exact routing the observation was taken through.
type Transport struct {
	Kind   string `json:"kind"`
	Value  string `json:"value,omitempty"`
	Source string `json:"source"`
}

// Unavailability is one scope of the observation that could not be taken.
type Unavailability struct {
	Scope  string `json:"scope"`
	Reason string `json:"reason"`
}

// Report is one runtime projection: what one exact server is running, of one
// object kind, plus everything that could not be observed.
//
// Items is never null so a consumer can iterate an empty report without a nil
// check. An empty Items with a populated Unavailable is the honest answer
// outside tmux; an empty Items with an empty Unavailable means the server is
// genuinely running nothing of that kind.
type Report struct {
	APIVersion  string           `json:"apiVersion"`
	Kind        string           `json:"kind"`
	Transport   Transport        `json:"transport"`
	HostMode    string           `json:"hostMode"`
	Unavailable []Unavailability `json:"unavailable,omitempty"`
	Items       []Row            `json:"items"`
}

// Observed reports whether this projection saw a server at all.
func (r Report) Observed() bool {
	return r.Transport.Kind != "" && r.Transport.Kind != string(resourcegraph.TransportNone)
}

// listKinds pins the JSON `kind` of each object kind's report. It is a closed
// switch rather than a derived string so a renamed tmux object kind cannot
// silently rename a published schema.
func listKind(kind resourcegraph.ObjectKind) string {
	switch kind {
	case resourcegraph.ObjectSession:
		return "RuntimeSessionList"
	case resourcegraph.ObjectWindow:
		return "RuntimeWindowList"
	case resourcegraph.ObjectPane:
		return "RuntimePaneList"
	default:
		return ""
	}
}

// ListKind returns the published JSON kind of one object kind's report, or "".
func ListKind(kind resourcegraph.ObjectKind) string { return listKind(kind) }

// Project builds the report of one object kind from a resolved graph.
//
// The graph's own order is preserved. Resolve emits observed objects in
// ascending tmux id, which is both stable across observations and the order a
// server grew in, so no second sort is applied here.
func Project(graph resourcegraph.Graph, kind resourcegraph.ObjectKind) Report {
	report := Report{
		APIVersion: APIVersion,
		Kind:       listKind(kind),
		Transport: Transport{
			Kind:   string(graph.Transport.Kind),
			Value:  graph.Transport.Value,
			Source: string(graph.Transport.Source),
		},
		HostMode: string(graph.HostMode),
		Items:    []Row{},
	}
	for _, entry := range graph.Unavailable {
		report.Unavailable = append(report.Unavailable, Unavailability{
			Scope: string(entry.Scope), Reason: entry.Reason,
		})
	}
	index := newIndex(graph)
	for _, node := range graph.Runtime {
		if node.Ref.Kind != kind {
			continue
		}
		report.Items = append(report.Items, index.row(node))
	}
	return report
}

// Rows returns every observed object of every kind in containment order --
// sessions, then windows, then panes -- which is the order a topology reads in.
//
// It is what the diagnostics picker lists. A picker that projected one kind at
// a time would make an operator run three passes to answer "what is on this
// server", which is the one question the surface exists for.
func Rows(graph resourcegraph.Graph) []Row {
	index := newIndex(graph)
	out := make([]Row, 0, len(graph.Runtime))
	for _, kind := range resourcegraph.ObjectKinds() {
		for _, node := range graph.Runtime {
			if node.Ref.Kind != kind {
				continue
			}
			out = append(out, index.row(node))
		}
	}
	return out
}

// Unavailable returns the graph's unavailable scopes in observation order.
func Unavailable(graph resourcegraph.Graph) []Unavailability {
	out := make([]Unavailability, 0, len(graph.Unavailable))
	for _, entry := range graph.Unavailable {
		out = append(out, Unavailability{Scope: string(entry.Scope), Reason: entry.Reason})
	}
	return out
}

// Counts returns how many observed objects carry each class, in the closed
// declaration order of the attribution set. A class with no objects is omitted,
// so a summary line names only what is actually there.
func Counts(rows []Row) []ClassCount {
	out := make([]ClassCount, 0, len(resourcegraph.Classes()))
	for _, class := range resourcegraph.Classes() {
		total := 0
		for _, row := range rows {
			if row.Class == string(class) {
				total++
			}
		}
		if total > 0 {
			out = append(out, ClassCount{Class: string(class), Count: total})
		}
	}
	return out
}

// ClassCount is one attribution class and how many observed objects carry it.
type ClassCount struct {
	Class string `json:"class"`
	Count int    `json:"count"`
}

// index resolves the containment chain and the Registry-side identity of a
// runtime node once per report instead of once per row.
type index struct {
	sessionName map[string]string
	sessionOf   map[string]string
	windowOf    map[string]string
	resource    map[string]Resource
	conflicts   map[string][]Conflict
}

func newIndex(graph resourcegraph.Graph) *index {
	idx := &index{
		sessionName: map[string]string{},
		sessionOf:   map[string]string{},
		windowOf:    map[string]string{},
		resource:    map[string]Resource{},
		conflicts:   map[string][]Conflict{},
	}
	for _, node := range graph.Runtime {
		switch node.Ref.Kind {
		case resourcegraph.ObjectSession:
			idx.sessionName[node.Ref.ID] = node.Ref.Name
			idx.sessionOf[node.Ref.ID] = node.Ref.ID
		case resourcegraph.ObjectWindow:
			idx.sessionOf[node.Ref.ID] = node.ContainerID
		case resourcegraph.ObjectPane:
			idx.windowOf[node.Ref.ID] = node.ContainerID
		}
	}
	for _, project := range graph.Projects {
		idx.resource[project.Project.Metadata.UID] = Resource{
			Kind: string(coremetadata.KindProject),
			UID:  project.Project.Metadata.UID,
			Name: project.Project.Metadata.Name,
		}
	}
	for _, window := range graph.Windows {
		idx.resource[window.Window.Metadata.UID] = Resource{
			Kind: string(coremetadata.KindWindow),
			UID:  window.Window.Metadata.UID,
			Name: window.Window.Metadata.Name,
		}
	}
	for _, pane := range graph.Panes {
		idx.resource[pane.Pane.Metadata.UID] = Resource{
			Kind: string(coremetadata.KindPane),
			UID:  pane.Pane.Metadata.UID,
			Name: pane.Pane.Metadata.Name,
		}
	}
	for _, conflict := range graph.Conflicts {
		entry := Conflict{
			UID:    conflict.UID,
			Reason: string(conflict.Reason),
			Detail: conflict.Detail,
		}
		if len(conflict.Targets) > 0 {
			entry.Targets = slices.Clone(conflict.Targets)
		}
		for _, target := range conflict.Targets {
			idx.conflicts[target] = append(idx.conflicts[target], entry)
		}
	}
	return idx
}

// sessionID walks the containment chain up to the enclosing session id.
//
// It walks tmux's own ids rather than joining on a name: two servers can hold
// the same session name and one server can rename a session between two
// observations, and neither may re-parent a window here.
func (i *index) sessionID(node resourcegraph.RuntimeNode) string {
	switch node.Ref.Kind {
	case resourcegraph.ObjectSession:
		return node.Ref.ID
	case resourcegraph.ObjectWindow:
		return node.ContainerID
	case resourcegraph.ObjectPane:
		return i.sessionOf[node.ContainerID]
	default:
		return ""
	}
}

// target builds the fully qualified exact coordinate of one object.
//
// The session half falls back from the observed name to the `$N` session id,
// which tmux resolves just as well and cannot be renamed out from under a later
// call. What it never does is omit the session: a window or pane coordinate
// with no session in front of it would be read by the focus grammar as a
// session name, so an object whose enclosing session could not be resolved --
// a pane observed while the windows query failed -- gets no target at all
// rather than one that would land somewhere else entirely.
func (i *index) target(node resourcegraph.RuntimeNode, sessionID string) string {
	session := strings.TrimSpace(i.sessionName[sessionID])
	if session == "" {
		session = strings.TrimSpace(sessionID)
	}
	switch node.Ref.Kind {
	case resourcegraph.ObjectSession:
		if session == "" {
			return strings.TrimSpace(node.Ref.ID)
		}
		return session
	case resourcegraph.ObjectWindow:
		if session == "" {
			return ""
		}
		return session + ":" + node.Ref.ID
	case resourcegraph.ObjectPane:
		window := strings.TrimSpace(node.ContainerID)
		if session == "" || window == "" {
			return ""
		}
		return session + ":" + window + "." + node.Ref.ID
	default:
		return ""
	}
}

func (i *index) row(node resourcegraph.RuntimeNode) Row {
	sessionID := i.sessionID(node)
	row := Row{
		Kind:        string(node.Ref.Kind),
		ID:          node.Ref.ID,
		Target:      i.target(node, sessionID),
		Name:        node.Ref.Name,
		Class:       string(node.Class),
		UID:         node.UID,
		ContainerID: node.ContainerID,
		SessionID:   sessionID,
		Reason:      node.Reason,
	}
	if node.ResourceUID != "" {
		if resource, ok := i.resource[node.ResourceUID]; ok {
			out := resource
			row.Resource = &out
		} else {
			row.Resource = &Resource{UID: node.ResourceUID}
		}
	}
	// A managed binding carries no reason in the graph, where the uid match is
	// self-evident. It needs one here: this surface exists to answer "why does
	// projmux say that", and "managed" with a blank reason next to six rows
	// that each explain themselves reads as a missing answer rather than an
	// obvious one.
	if row.Reason == "" && node.Class == resourcegraph.ClassManaged {
		row.Reason = "bound to " + managedBindingRef(row) + " by mirrored uid " + node.UID
	}
	if conflicts, ok := i.conflicts[node.Ref.ID]; ok {
		row.Conflicts = slices.Clone(conflicts)
	}
	return row
}

// managedBindingRef names the resource a managed row is bound to, in the
// `kind/name` spelling the resource routes use.
func managedBindingRef(row Row) string {
	if row.Resource == nil {
		return "an unlisted Registry resource"
	}
	kind := strings.TrimSpace(row.Resource.Kind)
	name := strings.TrimSpace(row.Resource.Name)
	switch {
	case kind != "" && name != "":
		return kind + "/" + name
	case name != "":
		return name
	case kind != "":
		return kind
	default:
		return row.Resource.UID
	}
}
