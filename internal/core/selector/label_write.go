package selector

import (
	"slices"
	"strings"
)

// LabelRemovalSuffix is the trailing token that spells a removal in the label
// write grammar: `key-` removes the key, `key=value` sets it, and `key=` sets
// it to the empty string.
//
// The write grammar is deliberately not ParseLabel's. A read condition is
// always `key=value`, so ParseLabel refuses every operand without `=` and can
// never accept a removal. Splitting them leaves the read grammar exactly as it
// was while giving the write path the one spelling it needs.
const LabelRemovalSuffix = "-"

// LabelAssignmentSeparator splits a set operand into its key and value.
const LabelAssignmentSeparator = "="

// LabelChange is one requested label mutation.
//
// Remove reports which half of the grammar produced it: a removal carries only
// a Key, and a set carries a Key and the Value to store, which may be empty.
type LabelChange struct {
	Key    string
	Value  string
	Remove bool
}

// IsLabelChangeOperand reports whether raw has the shape of a label change
// rather than of a resource reference.
//
// The shape decides, never the Registry: a token carrying `=` or ending in `-`
// is always a label change. That is what keeps the grammar readable without
// consulting what happens to exist, and `=` can never be part of a resource
// name at all, because metadata.ValidateName rejects that rune. A trailing `-`
// is a legal name rune, so a resource whose name ends in one is addressed by
// `uid:` or by its scope flag rather than positionally; the route says so.
func IsLabelChangeOperand(raw string) bool {
	return strings.Contains(raw, LabelAssignmentSeparator) || strings.HasSuffix(raw, LabelRemovalSuffix)
}

// ParseLabelChange parses one label write operand.
//
// Keys and values are trimmed exactly the way ParseLabel trims a read
// condition. The two normalizations have to agree, or a written label would be
// unselectable by the `--selector` spelling that wrote it.
func ParseLabelChange(raw string) (LabelChange, error) {
	const op = "parse label"
	if key, value, ok := strings.Cut(raw, LabelAssignmentSeparator); ok {
		key = strings.TrimSpace(key)
		if key == "" {
			return LabelChange{}, inputErr(op, "label %q has an empty key", raw)
		}
		return LabelChange{Key: key, Value: strings.TrimSpace(value)}, nil
	}
	if key, ok := strings.CutSuffix(raw, LabelRemovalSuffix); ok {
		key = strings.TrimSpace(key)
		if key == "" {
			return LabelChange{}, inputErr(op, "label %q has an empty key", raw)
		}
		return LabelChange{Key: key, Remove: true}, nil
	}
	return LabelChange{}, inputErr(op, "label %q must be key=value to set a label or key- to remove one", raw)
}

// ParseLabelChanges parses every operand of one label write.
//
// A key named twice is refused rather than resolved by argv order: `role=a
// role=b` and `role=a role-` are both a request the operator did not mean, and
// quietly keeping the last one would store a label they never chose.
func ParseLabelChanges(raw []string) ([]LabelChange, error) {
	changes := make([]LabelChange, 0, len(raw))
	seen := make(map[string]string, len(raw))
	for _, operand := range raw {
		change, err := ParseLabelChange(operand)
		if err != nil {
			return nil, err
		}
		if previous, ok := seen[change.Key]; ok {
			return nil, inputErr("parse label",
				"label key %q is changed twice, by %q and %q", change.Key, previous, operand)
		}
		seen[change.Key] = operand
		changes = append(changes, change)
	}
	return changes, nil
}

// LabelChangeSets splits parsed changes into the set map and the removal list
// the metadata mutator takes.
func LabelChangeSets(changes []LabelChange) (map[string]string, []string) {
	var (
		set    map[string]string
		remove []string
	)
	for _, change := range changes {
		if change.Remove {
			remove = append(remove, change.Key)
			continue
		}
		if set == nil {
			set = make(map[string]string, len(changes))
		}
		set[change.Key] = change.Value
	}
	return set, remove
}

// FormatLabels renders a label map in stable key order for result text.
func FormatLabels(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+LabelAssignmentSeparator+labels[key])
	}
	return strings.Join(parts, " ")
}
