package cli

import (
	"slices"
	"strings"
)

// Replacement command lines of the deprecated `agent instructions|persona
// attach|detach` spellings. `agent relaunch --instructions` is the one way to
// change a started Agent's instructions; the namespace nodes, which a call
// reaches when it names neither verb, carry both forms.
const (
	deprecatedAttachReplacement       = "projmux agent relaunch <agent-ref> --instructions <name>"
	deprecatedDetachReplacement       = "projmux agent relaunch <agent-ref> --instructions none"
	deprecatedAttachDetachReplacement = "projmux agent relaunch <agent-ref> --instructions <name>|none"
)

// DeprecatedPersonaFlagNotice is the stderr line a create that is given
// `--persona` prints. A flag is not a graph node, so its notice is a sentence
// of its own rather than a Route.Deprecated value; it has the same shape as
// DeprecationNotice.
const DeprecatedPersonaFlagNotice = "projmux: --persona is deprecated; use `--instructions` instead."

// DeprecationNotice returns the one stderr line for argv that reaches a
// deprecated route, and false for every other argv.
//
// The deepest deprecated node on the resolved path owns the sentence, so
// `agent persona attach`, which is deprecated both for its noun and for its
// verb, prints one line: the replacement of `attach`. The line goes to stderr
// and never to stdout, because the stdout bytes of a deprecated spelling stay
// what they were while it was listed.
func DeprecationNotice(args []string) (string, bool) {
	lead := args
	if i := slices.Index(args, argumentTerminator); i >= 0 {
		lead = args[:i]
	}
	if len(lead) == 0 {
		return "", false
	}
	current, ok := LookupRoute(lead[0])
	if !ok {
		return "", false
	}
	path := []string{current.Name}
	spelling, replacement := deprecatedAt(path, current, "", "")
	for _, token := range lead[1:] {
		child, found := findChild(current, token)
		if !found {
			break
		}
		current = child
		path = append(path, child.Name)
		spelling, replacement = deprecatedAt(path, current, spelling, replacement)
	}
	if replacement == "" {
		return "", false
	}
	return "projmux: " + spelling + " is deprecated; use `" + replacement + "` instead.", true
}

func deprecatedAt(path []string, node Route, spelling, replacement string) (string, string) {
	if node.Deprecated == "" {
		return spelling, replacement
	}
	return strings.Join(path, " "), node.Deprecated
}

// plumbing reports whether node is hidden because users never type it, as
// opposed to a deprecated spelling, which is hidden from the listings and
// still answers help and usage like a public route.
func plumbing(node Route) bool {
	return node.Hidden && node.Deprecated == ""
}

// unlisted reports whether the spelling has a hidden node on its path, so it
// is in no listing.
func unlisted(spelling string) bool {
	tokens := strings.Fields(spelling)
	if len(tokens) == 0 {
		return false
	}
	current, ok := LookupRoute(tokens[0])
	if !ok {
		return false
	}
	if current.Hidden {
		return true
	}
	for _, token := range tokens[1:] {
		child, found := findChild(current, token)
		if !found {
			return false
		}
		if child.Hidden {
			return true
		}
		current = child
	}
	return false
}
