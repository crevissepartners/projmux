package focus

import "strings"

// Candidate represents one tmux session known to the server, ordered by
// most-recent activity first by the caller.
type Candidate struct {
	Name     string
	Attached bool
}

// Resolution describes the outcome of matching a requested target session
// against the live session inventory.
type Resolution struct {
	// Name is the session to act on (may differ from request when Fallback != "").
	Name string
	// Attached is true if Name is already attached on the server.
	Attached bool
	// Fallback is empty on an exact match, otherwise a short label describing
	// how Name was chosen (e.g. "prefix-match").
	Fallback string
}

// Resolve picks the session to focus given the requested name and the current
// inventory. Inventory must already be ordered most-recent-first.
//
// When the requested session is missing the resolver looks for the most
// recent session whose name shares a leading token (split on '-' or '_')
// with the request. If no such session exists, ok is false.
func Resolve(requested string, inventory []Candidate) (Resolution, bool) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return Resolution{}, false
	}

	for _, candidate := range inventory {
		if candidate.Name == requested {
			return Resolution{
				Name:     candidate.Name,
				Attached: candidate.Attached,
			}, true
		}
	}

	wantTokens := tokens(requested)
	if len(wantTokens) == 0 {
		return Resolution{}, false
	}

	bestIdx := -1
	bestShared := 0
	for i, candidate := range inventory {
		shared := sharedPrefixTokens(wantTokens, tokens(candidate.Name))
		if shared == 0 {
			continue
		}
		// Inventory is most-recent-first, so first hit at the maximum shared
		// length wins. Equal-length later candidates are older and lose.
		if shared > bestShared {
			bestShared = shared
			bestIdx = i
		}
	}
	if bestIdx < 0 {
		return Resolution{}, false
	}

	chosen := inventory[bestIdx]
	return Resolution{
		Name:     chosen.Name,
		Attached: chosen.Attached,
		Fallback: "prefix-match",
	}, true
}

func tokens(name string) []string {
	if name == "" {
		return nil
	}
	parts := strings.FieldsFunc(name, func(r rune) bool {
		return r == '-' || r == '_'
	})
	out := parts[:0]
	for _, p := range parts {
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func sharedPrefixTokens(a, b []string) int {
	limit := min(len(b), len(a))
	for i := 0; i < limit; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return limit
}
