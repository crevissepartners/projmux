package projmux

import (
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// ParseLabelCondition parses one key=value label condition exactly as the CLI
// parses one --selector occurrence: the key and value lose surrounding space,
// the value may be empty, the key may not. A failure is a CodeSelectorInvalid
// Refusal whose Error text is the CLI's own message, --selector wording
// included. It reads nothing and needs no Client.
func ParseLabelCondition(raw string) (core.LabelCondition, error) {
	label, err := selector.ParseLabel(raw)
	if err != nil {
		return core.LabelCondition{}, &Refusal{Code: CodeSelectorInvalid, message: err.Error(), cause: err}
	}
	return core.LabelCondition{Key: label.Key, Value: label.Value}, nil
}

// AgentsMatchingLabels returns the uids of the Agents of the Project projectUID
// whose labels satisfy every condition, in the order of the CLI's
// get agents --project uid:<projectUID> --selector ... Zero conditions return
// every Agent of the Project and no match returns an empty slice. projectUID is
// an exact uid, never a name. It reads only the held rows, never the Registry
// file or tmux, and changes nothing.
//
// A handle that was not prepared is a CodePreparedSnapshotInvalid refusal. An
// empty projectUID is CodeSelectorInvalid, and a Project that is not in the
// rows is CodeResourceNotFound. The caller owns the returned slice.
func (p *PreparedRegistrySnapshot) AgentsMatchingLabels(projectUID string, conditions []core.LabelCondition) ([]string, error) {
	if !p.valid() {
		return nil, &Refusal{Code: CodePreparedSnapshotInvalid, message: "prepared Registry snapshot was not prepared by PrepareRegistrySnapshot"}
	}
	if projectUID == "" {
		return nil, &Refusal{Code: CodeSelectorInvalid, message: "project uid must not be empty"}
	}
	query := selector.Query{Project: &selector.Ref{Kind: metadata.KindProject, UID: projectUID, Raw: selector.UIDPrefix + projectUID}}
	for _, condition := range conditions {
		query.Labels = append(query.Labels, selector.Label{Key: condition.Key, Value: condition.Value})
	}
	resolution, err := selector.New(p.registry).ResolveAgents(query)
	if err != nil {
		return nil, resourceRefusal(err)
	}
	return resolution.UIDs(), nil
}
