package core

// LabelCondition is one parsed key=value label condition. The key is never
// empty; the value may be empty and then matches a label set to the empty
// string. projmux.ParseLabelCondition is the parser that makes one.
type LabelCondition struct {
	Key   string
	Value string
}

// String renders the condition as key=value.
func (l LabelCondition) String() string { return l.Key + "=" + l.Value }
