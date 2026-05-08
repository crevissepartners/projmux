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
	label := strings.TrimSpace(stripANSISequences(i.EffectiveLabel()))
	value := strings.TrimSpace(i.Value)
	if value == "" || value == label {
		return label
	}
	if label == "" {
		return value
	}
	return label + "\t" + value
}

func stripANSISequences(value string) string {
	var out strings.Builder
	for i := 0; i < len(value); {
		if value[i] != '\x1b' {
			out.WriteByte(value[i])
			i++
			continue
		}
		if i+1 >= len(value) {
			break
		}
		if value[i+1] == '[' {
			i += 2
			for i < len(value) {
				b := value[i]
				i++
				if b >= 0x40 && b <= 0x7e {
					break
				}
			}
			continue
		}
		i += 2
	}
	return out.String()
}
