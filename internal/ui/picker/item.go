package picker

import "strings"

// Item is the backend-neutral representation of a selectable picker row.
type Item struct {
	Label         string
	Title         string
	Value         string
	State         string
	SearchText    string
	MetaLines     []string
	Badges        []string
	PreviewTarget string
}

func (i Item) EffectiveLabel() string {
	if label := strings.TrimSpace(i.Label); label != "" {
		return label
	}
	if title := strings.TrimSpace(i.Title); title != "" {
		return title
	}
	return strings.TrimSpace(i.Value)
}

func (i Item) EffectiveSearchText() string {
	if search := strings.TrimSpace(i.SearchText); search != "" {
		return search
	}
	return strings.TrimSpace(i.Title)
}
