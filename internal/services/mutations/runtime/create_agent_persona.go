package runtime

import (
	"fmt"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"maps"
)

// PersonaLaunch is the persona one Agent create starts with: the stored Name,
// the Snapshot the provider is given, and that Snapshot's Content. The zero
// value means no persona.
//
// Both forms of the same bytes are kept because the two providers take the
// persona differently and neither should have to read the Snapshot back:
// Claude is given the Snapshot path on its command line, and Codex is given
// the Content itself as the thread's developer instructions, because putting
// it in argv would publish it to every reader of `ps`.
type PersonaLaunch struct {
	Name     string
	Snapshot persona.Snapshot
	Content  string
}

// WithAnnotations adds the two keys that link the new Agent to its persona to
// base, the Agent annotations the create already records (the creator keys).
// Without a persona it returns base itself, so a create without --persona
// stores exactly what it stored before the flag existed -- nil included.
// Otherwise it returns a new map and never writes into base.
func (p PersonaLaunch) WithAnnotations(base map[string]string) map[string]string {
	if p.Name == "" {
		return base
	}
	out := maps.Clone(base)
	if out == nil {
		out = make(map[string]string, 2)
	}
	out[coremetadata.AnnotationAgentPersona] = p.Name
	out[coremetadata.AnnotationAgentPersonaDigest] = p.Snapshot.Digest
	return out
}

// PreparePersonaLaunch resolves the named persona and writes its Snapshot.
//
// It runs after every argv-only refusal and after scope resolution, and before
// the create transaction opens: a persona that is missing, too large, or badly
// named refuses with its reason token while nothing exists, and a Snapshot
// that cannot be written refuses the same way. A later failure can leave the
// Snapshot behind, which is harmless: it is Content addressed.
func (c *AgentSettings) PreparePersonaLaunch(spelling, Name string, optionNames ...string) (PersonaLaunch, error) {
	store, loaded, option, err := c.loadPersonaLaunch(spelling, Name, optionNames...)
	if err != nil {
		return PersonaLaunch{}, err
	}
	Snapshot, err := store.WriteSnapshot(loaded.Content)
	if err != nil {
		return PersonaLaunch{}, fmt.Errorf("%s %s: %w; nothing was created", spelling, c.Fields.Option(option), err)
	}
	return PersonaLaunch{Name: loaded.Name, Snapshot: Snapshot, Content: string(loaded.Content)}, nil
}

// CheckPersonaLaunch is the read half of PreparePersonaLaunch: it gives the
// same refusal for a persona that is missing, too large, or badly named, and
// writes no Snapshot.
func (c *AgentSettings) CheckPersonaLaunch(spelling, Name string, optionNames ...string) error {
	_, _, _, err := c.loadPersonaLaunch(spelling, Name, optionNames...)
	return err
}

func (c *AgentSettings) loadPersonaLaunch(spelling, Name string, optionNames ...string) (persona.Store, persona.Persona, string, error) {
	option := "instructions"
	if len(optionNames) > 0 && optionNames[0] != "" {
		option = optionNames[0]
	}
	paths, err := c.Paths()
	if err != nil {
		return persona.Store{}, persona.Persona{}, option, fmt.Errorf("%s %s: %w; nothing was created", spelling, c.Fields.Option(option), err)
	}
	store := persona.NewDefaultStore(paths)
	loaded, err := store.Load(Name)
	if err != nil {
		if persona.ReasonOf(err) != "" {
			// The deprecated --persona keeps its old noun.
			return store, persona.Persona{}, option, createAgentUsage(persona.ReasonOf(err), fmt.Sprintf("%s %s: %v; nothing was created", spelling, c.Fields.Option(option), instructionsRefusalNoun(err, option)), err)
		}
		return store, persona.Persona{}, option, fmt.Errorf("%s %s: %w; nothing was created", spelling, c.Fields.Option(option), err)
	}
	return store, loaded, option, nil
}

func instructionsRefusalNoun(err error, option string) error {
	if option == persona.DeprecatedNoun {
		return persona.SpelledAs(err, persona.DeprecatedNoun)
	}
	return err
}
