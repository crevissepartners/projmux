package runtime

import (
"fmt"
"github.com/crevissepartners/projmux/internal/core/persona"
"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
"maps"
"regexp"
"slices"
"strings"
coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// personaLaunch is the persona one Agent create starts with: the stored name,
// the snapshot the provider is given, and that snapshot's content. The zero
// value means no persona.
//
// Both forms of the same bytes are kept because the two providers take the
// persona differently and neither should have to read the snapshot back:
// Claude is given the snapshot path on its command line, and Codex is given
// the content itself as the thread's developer instructions, because putting
// it in argv would publish it to every reader of `ps`.
type personaLaunch struct {
	name     string
	snapshot persona.Snapshot
	content  string
}

// withAnnotations adds the two keys that link the new Agent to its persona to
// base, the Agent annotations the create already records (the creator keys).
// Without a persona it returns base itself, so a create without --persona
// stores exactly what it stored before the flag existed -- nil included.
// Otherwise it returns a new map and never writes into base.
func (p personaLaunch) withAnnotations(base map[string]string) map[string]string {
	if p.name == "" {
		return base
	}
	out := maps.Clone(base)
	if out == nil {
		out = make(map[string]string, 2)
	}
	out[coremetadata.AnnotationAgentPersona] = p.name
	out[coremetadata.AnnotationAgentPersonaDigest] = p.snapshot.Digest
	return out
}

// preparePersonaLaunch resolves the named persona and writes its snapshot.
//
// It runs after every argv-only refusal and after scope resolution, and before
// the create transaction opens: a persona that is missing, too large, or badly
// named refuses with its reason token while nothing exists, and a snapshot
// that cannot be written refuses the same way. A later failure can leave the
// snapshot behind, which is harmless: it is content addressed.
func (c *createCommand) preparePersonaLaunch(spelling, name string, optionNames ...string) (personaLaunch, error) {
	option := "instructions"
	if len(optionNames) > 0 && optionNames[0] != "" {
		option = optionNames[0]
	}
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return personaLaunch{}, fmt.Errorf("%s --%s: %w; nothing was created", spelling, option, err)
	}
	store := persona.NewDefaultStore(paths)
	loaded, err := store.Load(name)
	if err != nil {
		if persona.ReasonOf(err) != "" {
			// The deprecated --persona keeps its old noun.
			return personaLaunch{}, usageError(fmt.Sprintf("%s --%s: %v; nothing was created", spelling, option, personaRefusalNoun(err, option)))
		}
		return personaLaunch{}, fmt.Errorf("%s --%s: %w; nothing was created", spelling, option, err)
	}
	snapshot, err := store.WriteSnapshot(loaded.Content)
	if err != nil {
		return personaLaunch{}, fmt.Errorf("%s --%s: %w; nothing was created", spelling, option, err)
	}
	return personaLaunch{name: loaded.Name, snapshot: snapshot, content: string(loaded.Content)}, nil
}
